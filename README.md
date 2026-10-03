# gridcore

Venue-agnostic, long-only grid strategy engine shared by the `*-trade`
perpetual DEX bots.

**Provenance:** extracted verbatim from `rh-lighter-trade`, the implementation
with no known bugs. `rh-lighter-trade` is the **single source of truth** for the
trading logic. If this package and any bot disagree, this package is right — fix
the bot, never fork the engine.

The package is pure Go stdlib (zero third-party dependencies) and is safe to
import from any venue module without pulling in another DEX's SDK.

---

## Read this first (instructions for an AI integrating a new venue)

You are being asked to add support for a new perp DEX/CEX. **Do not re-derive
or "improve" the grid strategy.** The strategy, the fill/re-arm state machine,
startup recovery, TP coverage repair and expiry handling are already
implemented and tested here. Your job is a thin adapter:

1. Implement `gridcore.Venue` (7 methods) against the venue SDK/API.
2. Optionally implement `gridcore.Streamer` for WebSocket account updates.
3. Write `cmd/<dex>/main.go` that loads config and calls
   `gridcore.New(...).Run(ctx)`.
4. Make `go test ./...` pass. The conformance suite in this module (90 tests)
   is the contract. Do not copy strategy code into the adapter to make them pass.

If you find yourself editing grid math, the recovery ladder, coverage
accounting or the fill state machine, **stop** — you are on the wrong path.

---

## Architecture

```
gridcore (this module)                 internal/<dex>  (your adapter)
─────────────────────────────          ────────────────────────────────
grid math, TP/recovery ladders   <──   implements Venue
fill → TP → re-arm state machine <──   maps venue JSON → core types
startup recovery + TP coverage   <──   signing / auth / nonces / REST / WS
expiry / accidental-price retry  <──   (optional) implements Streamer
client-order-id encoding         <──   uses Encode*ClientOrderID
telegram + free-margin monitor
cmd main orchestration (Run)
```

The engine calls `Venue` from two goroutines (REST reconcile loop and the WS
event path), so implementations must be safe for concurrent use.

---

## The `Venue` contract

```go
type Venue interface {
	Market(ctx context.Context) (MarketMeta, error)
	Account(ctx context.Context) (AccountResponse, error)
	ActiveOrders(ctx context.Context) ([]Order, error)
	OrdersByClientIndexes(ctx context.Context, clientIDs []int64) ([]Order, error)
	CancelAll(ctx context.Context) error
	FreeMarginPct(ctx context.Context) (float64, error)
	PlaceLimitOrder(ctx context.Context, side string, level GridLevel, sizeAtomic int64, priceDecimals uint8, reduceOnly bool) (SendTxResponse, int64, error)
}
```

Method-by-method requirements:

- `Market` — return the configured market's metadata. `MarkPrice` is a decimal
  string; `PriceDecimals`/`SizeDecimals` are the venue's precision. The engine
  reads `MarkPrice` at startup and during coverage repair.
- `Account` — return positions. The engine reads the position for `MarketMeta.MarketID`
  and treats `Sign < 0` as a fatal short (this bot is long-only).
- `ActiveOrders` — every open order for the account. The engine classifies them
  by decoding the client order index (see below).
- `OrdersByClientIndexes` — batch lookup by the client order indexes the engine
  generated. Used for fill/expiry reconciliation.
- `CancelAll` — cancel every open order for the market. Called at startup and
  shutdown.
- `FreeMarginPct` — free margin as a percentage of equity; used for the <25%
  Telegram warning.
- `PlaceLimitOrder` — submit a LIMIT order. For `side == "buy"` use
  `level.BuyPrice`; for `side == "sell"` use `level.TPPrice`. Honor
  `reduceOnly` on sells. Return the venue response and the **client order index
  you encoded** (see below). `sizeAtomic` is already in integer atomic units.

### Client order indexes are the tracking key

Encode the client order index of every order with:

```go
clientID := gridcore.EncodeBuyClientOrderID(level.Index)  // buys
clientID := gridcore.EncodeTPClientOrderID(level.Index)   // reduce-only TP sells
```

Return that same `clientID` from `PlaceLimitOrder`. After a stateless restart
the engine decodes it via `DecodeClientOrderID` to attribute orders back to grid
levels. Sucessive calls for the same level return distinct ids, so re-places do
not collide. **Do not invent your own client-id scheme.**

### Optional: `Streamer`

```go
type Streamer interface {
	Run(ctx context.Context, marketID int16, onState func(connected bool), onUpdate func(AccountMarketUpdate)) error
}
```

Return an error only on fatal failure; reconnect internally with backoff. When
the streamer is connected, the engine skips REST polling of tracked orders
(but still runs position-coverage repair). Pass `nil` if the venue has no
account WS — the engine falls back to REST polling.

### Optional: `PriceBandClassifier`

Venues that reject prices outside an allowed band can implement:

```go
type PriceBandClassifier interface{ IsPriceBandError(err error) bool }
```

The engine then retries the TP with a band-safe fallback price instead of using
the built-in Lighter heuristic. Without it, the Lighter heuristic
(`"accidental price"` / code `21733`) is used, which simply never matches.

---

## Exported helpers for adapters

| Function | Purpose |
| --- | --- |
| `EncodeBuyClientOrderID(level)` | client index for a BUY at a grid level |
| `EncodeTPClientOrderID(level)` | client index for a reduce-only TP SELL |
| `DecodeClientOrderID(id)` | `(level, side, ok)` from a client index |
| `SizeToAtomic(size, decimals)` | base size → integer atomic units |
| `PriceToAtomic(price, decimals)` | price → integer atomic units |
| `AtomicToPrice(amount, decimals)` | atomic price → float |
| `AtomicToSize(amount, decimals)` | atomic size → float |
| `RoundPrice(price, decimals)` | round to venue price precision |
| `ResolveSymbol(symbol)` | `"BTC-USDT-PERP"` → `"BTC"` |

---

## Canonical rules (pinned by the test suite — do not change)

- Compounding buy ladder: `buy_k = mark * (1 - spacing/100)^(k+1)`, `k` 0-based.
- Per-fill TP: `tp = buy * (1 + profit/100)`.
- Startup/recovery ladder: anchored at `topBuy * (1 + profit/100)`, climbing by
  `(1 + spacing/100)` per unit — **spacing, not profit**. Several other bots in
  this repo family incorrectly climb by profit; that is a bug against this engine.
- Startup recovery TPs are lifted above `mark` and nudged strictly upward in
  atomic price units (post-only-safe upward ladder, not a compressed band).
- A recovered TP restores its implied buy as `round(tp / (1 + profit/100))`.
- A buy fill places its TP from the **tracked order price**, never the grid level.
- A TP fill restores the buy at its **tracked** buy price.
- An expired/canceled order is re-posted at the **same tracked price**.
- Coverage is counted in position **units** (`activeTPUnits + inFlightTPUnits`);
  multiple TPs on one level each count, so a covered position is never re-placed.
- Anti-churn windows: `inFlightTPWindow = 90s`, `coveragePlaceCooldown = 60s`.
- Long only: a short position is a fatal error.
- Stateless: every startup cancels all orders and rebuilds from the position.
- All orders are LIMIT; TP sells are reduce-only.

---

## Integrating a new venue — step by step

1. **Module setup.** Create/point the venue module at this package:

   ```bash
   go get github.com/web3frozen-devops/gridcore@latest
   ```

2. **Types.** Reuse the core types directly. Define aliases instead of parallel
   copies so your client satisfies `Venue` with no mapping code:

   ```go
   type MarketMeta       = gridcore.MarketMeta
   type Order            = gridcore.Order
   type AccountResponse  = gridcore.AccountResponse
   type AccountPosition  = gridcore.AccountPosition
   type SendTxResponse   = gridcore.SendTxResponse
   type GridLevel        = gridcore.GridLevel
   type AccountMarketUpdate = gridcore.AccountMarketUpdate
   ```

3. **Implement `Venue`.** One file, `internal/<dex>/exchange.go`. Map the
   venue's REST/SDK calls into the aliased types, encode client ids with the
   helpers above, and return them from `PlaceLimitOrder`.

4. **Implement `Streamer` (optional).** `internal/<dex>/ws.go`. Reconnect with
   backoff, invoke the `onState`/`onUpdate` callbacks you are given.

5. **Config.** Use `gridcore.LoadConfig()` for the shared grid settings
   (`ORDER_SIZE`, `GRID_SPACING_PERCENTAGE`, `PROFIT_PERCENTAGE`,
   `NUM_GRID_LEVELS`, `POLL_INTERVAL_SECONDS`, `MARGIN_CHECK_INTERVAL_SECONDS`,
   `DRY_RUN`, `PRE_RUN`, telegram, …). Load venue-only settings (URLs, chain
   ids, keys, market id, order expiry, auth/retry tuning) in the adapter.

6. **Wire `main.go`.** `gridcore.LoadDotEnv(".env")`, `gridcore.LoadConfig()`,
   construct your venue, then:

   ```go
   bot := gridcore.New(cfg, logger, venue, streamer, telegram, accountIndex)
   if err := bot.Run(ctx); err != nil { logger.Error("bot exited", "error", err) }
   ```

7. **Verify.** From the venue module: `go build ./... && go test ./...`. From
   this module: `go test ./...` (90 tests). Never weaken or delete the conformance
   tests.

---

## Adapter skeleton

```go
package mydex

import (
	"context"

	"github.com/web3frozen-devops/gridcore"
)

type Exchange struct { /* client, market, account index, auth ... */ }

func (e *Exchange) Market(ctx context.Context) (gridcore.MarketMeta, error)           { /* ... */ }
func (e *Exchange) Account(ctx context.Context) (gridcore.AccountResponse, error)     { /* ... */ }
func (e *Exchange) ActiveOrders(ctx context.Context) ([]gridcore.Order, error)        { /* ... */ }
func (e *Exchange) OrdersByClientIndexes(ctx context.Context, ids []int64) ([]gridcore.Order, error) { /* ... */ }
func (e *Exchange) CancelAll(ctx context.Context) error                               { /* ... */ }
func (e *Exchange) FreeMarginPct(ctx context.Context) (float64, error)                { /* ... */ }

func (e *Exchange) PlaceLimitOrder(ctx context.Context, side string, lvl gridcore.GridLevel, sizeAtomic int64, priceDecimals uint8, reduceOnly bool) (gridcore.SendTxResponse, int64, error) {
	var clientID int64
	price := lvl.BuyPrice
	if side == "buy" {
		clientID = gridcore.EncodeBuyClientOrderID(lvl.Index)
	} else {
		price = lvl.TPPrice
		clientID = gridcore.EncodeTPClientOrderID(lvl.Index)
	}
	// submit LIMIT order at `price`, size `sizeAtomic` (atomic), reduceOnly...
	return resp, clientID, nil
}
```

---

## Common pitfalls

- **Reimplementing strategy logic in the adapter.** Don't. Only I/O belongs there.
- **Wrong client-id scheme.** Without `Encode*ClientOrderID`, fills/expiries
  cannot be attributed to levels after a restart.
- **Returning a different id than you encoded.** The engine tracks by the id you
  return from `PlaceLimitOrder`.
- **Not honoring `reduceOnly`** on TP sells — could open a short.
- **Ladder step.** Recovery ladder climbs by **spacing**, not profit.
- **Floating precision.** Keep tick/size rounding in atomic integer units via the
  exported helpers; do not round floats ad hoc.
- **Mutable state shared with the engine.** `Venue` methods are called from two
  goroutines; guard your own state.

## Consuming this module

This module is public, so no credentials are needed:

```bash
go get github.com/web3frozen-devops/gridcore@latest
```

## Tests

The suite (90 tests) was copied from `rh-lighter-trade` unchanged (minus
venue-specific config-field assertions) and pins: expiry replacement, startup
recovery, post-only-safe ladders, in-flight/coverage accounting, overflow
levels, accidental-price fallback, and TP-price selection from tracked state.

## CI (GitHub Actions)

Public module — no secrets, no `GOPRIVATE`, no git auth. A standard Go job just
works:

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: actions/setup-go@v5
    with: { go-version-file: go.mod, cache: true }
  - run: go test ./...
```

### What CI runs

All workflows use GitHub-provided actions and the official Go toolchain:

- `ci.yml` — `gofmt` gate, `go vet`, then `go test -race -covermode=atomic`
  with an **85% coverage floor** (currently ~90%). The profile is uploaded with
  official `actions/upload-artifact`. A second job (`consumer-smoke`) fetches
  this module as an external consumer, credential-free, to prove it is
  importable from a fresh project.
- `codeql.yml` — GitHub CodeQL Go analysis (`security-and-quality` query pack)
  on push, PR and weekly, uploading SARIF to GitHub code scanning.
- `security.yml` — `govulncheck` against the Go vulnerability database
  (push, PR, weekly) plus `actions/dependency-review-action` on PRs.
- `dependabot.yml` — weekly Go module and GitHub Actions update PRs.

Repo-level GitHub security features are enabled: CodeQL code scanning, Dependabot
vulnerability alerts and security updates, secret scanning, secret scanning push
protection, and private vulnerability reporting. See `.github/SECURITY.md`.

Docker builds need no token either:

```dockerfile
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gridbot ./cmd/gridbot
```

`.github/workflows/ci.yml` includes a `consumer-smoke` job that fetches this
module from a clean directory on every push, so module-fetch regressions fail CI
here first.

## Versioning

Semver tags (`v0.1.0`, …). Adapters pin a tag. Breaking changes to `Venue` or
the canonical rules bump the major version.
