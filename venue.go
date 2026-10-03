package gridcore

import "context"

// Venue is the entire surface a perpetual DEX must implement to run the grid
// engine. Everything else — grid math, the fill/re-arm state machine, recovery
// coverage, order expiry handling, risk alerts — lives in this package and is
// shared across venues, so a new DEX is a Venue adapter plus a cmd main.
//
// Implementations must be safe for concurrent use; the engine calls them from
// both the reconcile loop and the WebSocket event path.
type Venue interface {
	// Market returns venue metadata (tick/lot precision, mark price) for the
	// configured symbol. The engine calls it at startup and during coverage
	// recovery.
	Market(ctx context.Context) (MarketMeta, error)
	// Account returns the current position and balances.
	Account(ctx context.Context) (AccountResponse, error)
	// ActiveOrders returns every open order for the account.
	ActiveOrders(ctx context.Context) ([]Order, error)
	// OrdersByClientIndexes looks up orders by the client order indexes the
	// engine encoded, for fill/expiry reconciliation.
	OrdersByClientIndexes(ctx context.Context, clientIDs []int64) ([]Order, error)
	// CancelAll cancels every open order for the market.
	CancelAll(ctx context.Context) error
	// FreeMarginPct reports free margin as a percentage of equity.
	FreeMarginPct(ctx context.Context) (float64, error)
	// PlaceLimitOrder submits a LIMIT order and returns the venue response plus
	// the client order index assigned to it. reduceOnly must be honored for TP
	// sells.
	PlaceLimitOrder(ctx context.Context, side string, level GridLevel, sizeAtomic int64, priceDecimals uint8, reduceOnly bool) (SendTxResponse, int64, error)
}

// Streamer delivers account updates for one market. It is optional: when nil
// the engine falls back to REST polling only.
type Streamer interface {
	Run(ctx context.Context, marketID int16, onState func(connected bool), onUpdate func(AccountMarketUpdate)) error
}
