package gridcore

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type stubStreamer struct {
	started  chan struct{}
	stopped  chan struct{}
	marketID int16
	err      error
}

func (s *stubStreamer) Run(ctx context.Context, marketID int16, onState func(bool), onUpdate func(AccountMarketUpdate)) error {
	s.marketID = marketID
	if onState != nil {
		onState(true)
	}
	if s.started != nil {
		close(s.started)
	}
	<-ctx.Done()
	if onState != nil {
		onState(false)
	}
	if s.stopped != nil {
		close(s.stopped)
	}
	return s.err
}

type genericPlaceErrorVenue struct {
	stubExchange
	err error
}

func (g *genericPlaceErrorVenue) PlaceLimitOrder(ctx context.Context, side string, level GridLevel, sizeAtomic int64, priceDecimals uint8, reduceOnly bool) (SendTxResponse, int64, error) {
	return SendTxResponse{}, 0, g.err
}

func testBot(ex Venue) *Bot {
	return &Bot{
		cfg:        Config{ExchangeName: "Test", Symbol: "BTC-USDT-PERP", OrderSize: 0.01, ProfitPct: 0.2, GridSpacing: 0.2},
		logger:     slog.Default(),
		ex:         ex,
		market:     MarketMeta{MarketID: 1, MarkPrice: "100000", PriceDecimals: 2, SizeDecimals: 2},
		grid:       []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}, {Index: 1, BuyPrice: 99, TPPrice: 99.99}},
		tracked:    map[int64]*TrackedOrder{},
		seenTrades: map[int64]struct{}{},
	}
}

func TestNewInitializesState(t *testing.T) {
	b := New(Config{}, nil, nil, nil, nil, 42)
	if b.logger == nil {
		t.Fatal("expected default logger")
	}
	if b.tracked == nil || b.seenTrades == nil {
		t.Fatal("expected maps to be initialized")
	}
	if b.accountIndex != 42 {
		t.Fatalf("accountIndex = %d", b.accountIndex)
	}
}

func TestPollAndMarginIntervalOverrides(t *testing.T) {
	b := &Bot{cfg: Config{PollSeconds: 1, MarginSeconds: 1}}
	if got := b.pollInterval(); got != 2*time.Second {
		t.Fatalf("pollInterval floor = %v", got)
	}
	if got := b.marginInterval(); got != 5*time.Second {
		t.Fatalf("marginInterval floor = %v", got)
	}
	b.cfg.PollSeconds = 12
	b.cfg.MarginSeconds = 40
	if got := b.pollInterval(); got != 12*time.Second {
		t.Fatalf("pollInterval = %v", got)
	}
	if got := b.marginInterval(); got != 40*time.Second {
		t.Fatalf("marginInterval = %v", got)
	}
	b.pollEvery = 7 * time.Second
	b.marginEvery = 9 * time.Second
	if got := b.pollInterval(); got != 7*time.Second {
		t.Fatalf("pollInterval override = %v", got)
	}
	if got := b.marginInterval(); got != 9*time.Second {
		t.Fatalf("marginInterval override = %v", got)
	}
}

func TestRunDryRunBootstrapsAndStops(t *testing.T) {
	sx := &stubExchange{marketResp: MarketMeta{MarketID: 1, MarkPrice: "100000", PriceDecimals: 2, SizeDecimals: 2}}
	b := New(Config{
		ExchangeName: "Test", Symbol: "BTC-USDT-PERP", DryRun: true, DryRunMarkPrice: 100000,
		OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2, NumLevels: 2,
	}, slog.Default(), sx, nil, nil, 0)
	b.pollEvery = 5 * time.Millisecond
	b.marginEvery = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := b.Run(ctx); err != nil {
		t.Fatalf("Run dry-run = %v", err)
	}
	if sx.cancelAllHits != 0 {
		t.Fatalf("dry run must not cancel orders, got %d", sx.cancelAllHits)
	}
}

func TestRunNonDryRunPlacesGridAndCancelsOnShutdown(t *testing.T) {
	sx := &stubExchange{
		marketResp:  MarketMeta{MarketID: 1, MarkPrice: "100000", PriceDecimals: 2, SizeDecimals: 2},
		accountResp: AccountResponse{},
	}
	b := New(Config{
		ExchangeName: "Test", Symbol: "BTC-USDT-PERP",
		OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2, NumLevels: 2,
	}, slog.Default(), sx, nil, nil, 0)
	b.pollEvery = 5 * time.Millisecond
	b.marginEvery = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := b.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if countSide(sx.placed, "buy") != 2 {
		t.Fatalf("expected 2 buys, got %#v", sx.placed)
	}
	if sx.cancelAllHits < 2 {
		t.Fatalf("expected bootstrap+shutdown cancels, got %d", sx.cancelAllHits)
	}
}

func TestRunStartsStreamerAndTracksConnection(t *testing.T) {
	streamer := &stubStreamer{started: make(chan struct{})}
	sx := &stubExchange{marketResp: MarketMeta{MarketID: 3, MarkPrice: "100000", PriceDecimals: 2, SizeDecimals: 2}}
	b := New(Config{
		ExchangeName: "Test", Symbol: "BTC-USDT-PERP", DryRun: true, DryRunMarkPrice: 100000,
		OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2, NumLevels: 1,
	}, slog.Default(), sx, streamer, nil, 0)
	b.pollEvery = 5 * time.Millisecond
	b.marginEvery = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	select {
	case <-streamer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("streamer was not started")
	}
	if streamer.marketID != 3 {
		t.Fatalf("streamer marketID = %d", streamer.marketID)
	}
	b.mu.Lock()
	connected := b.wsConnected
	b.mu.Unlock()
	if !connected {
		t.Fatal("expected wsConnected true")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestRunBootstrapErrorPropagates(t *testing.T) {
	b := New(Config{DryRun: false, OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2, NumLevels: 1},
		slog.Default(), &stubExchange{marketResp: MarketMeta{}}, nil, nil, 0)
	// Empty market meta → size atomic 0 → bootstrap error.
	if err := b.Run(context.Background()); err == nil {
		t.Fatal("expected bootstrap error")
	}
}

func TestSetWSConnected(t *testing.T) {
	b := &Bot{}
	b.setWSConnected(true)
	b.mu.Lock()
	on := b.wsConnected
	b.mu.Unlock()
	if !on {
		t.Fatal("expected true")
	}
	b.setWSConnected(false)
	b.mu.Lock()
	off := b.wsConnected
	b.mu.Unlock()
	if off {
		t.Fatal("expected false")
	}
}

func TestSetPollCooldown(t *testing.T) {
	b := &Bot{pollEvery: time.Millisecond}
	b.setPollCooldown(nil)
	b.mu.Lock()
	zero := b.pollCooldownUntil.IsZero()
	b.mu.Unlock()
	if !zero {
		t.Fatal("nil error should not set cooldown")
	}

	b.setPollCooldown(errors.New("temporary failure"))
	b.mu.Lock()
	until := b.pollCooldownUntil
	b.mu.Unlock()
	if d := time.Until(until); d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("generic cooldown = %v", d)
	}

	b2 := &Bot{pollEvery: time.Millisecond}
	b2.setPollCooldown(errors.New(`{"code":429,"message":"rate limited"}`))
	b2.mu.Lock()
	until429 := b2.pollCooldownUntil
	b2.mu.Unlock()
	if d := time.Until(until429); d < 29*time.Second || d > 31*time.Second {
		t.Fatalf("429 cooldown = %v", d)
	}

	// A shorter later cooldown must not shorten an existing longer one.
	b2.setPollCooldown(errors.New("temporary failure"))
	b2.mu.Lock()
	still := b2.pollCooldownUntil
	b2.mu.Unlock()
	if !still.Equal(until429) {
		t.Fatalf("cooldown was shortened: %v -> %v", until429, still)
	}
}

func TestTrackedOrderPriceContext(t *testing.T) {
	b := &Bot{tracked: map[int64]*TrackedOrder{
		1: {ClientOrderID: 1, Side: "buy", Price: 100},
		2: {ClientOrderID: 2, Side: "sell", Price: 101},
	}}
	price, has, out := b.trackedOrderPriceContext([]int64{1, 999, 2})
	if !has || price != 100 {
		t.Fatalf("price = %v has = %v", price, has)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 tracked contexts, got %d", len(out))
	}
	_, has, out = b.trackedOrderPriceContext([]int64{999})
	if has || len(out) != 0 {
		t.Fatalf("unknown ids should yield nothing, got has=%v out=%d", has, len(out))
	}
}

func TestCheckFreeMargin(t *testing.T) {
	ctx := context.Background()
	dry := &Bot{cfg: Config{DryRun: true}, logger: slog.Default()}
	if err := dry.checkFreeMargin(ctx); err != nil {
		t.Fatalf("dry-run margin check = %v", err)
	}

	sx := &stubExchange{freeMarginPct: 10}
	b := &Bot{cfg: Config{ExchangeName: "Test"}, logger: slog.Default(), ex: sx}
	if err := b.checkFreeMargin(ctx); err != nil {
		t.Fatalf("checkFreeMargin = %v", err)
	}
	if !b.freeMarginWarned {
		t.Fatal("expected warning latch for low margin")
	}
	if err := b.checkFreeMargin(ctx); err != nil {
		t.Fatalf("second checkFreeMargin = %v", err)
	}
	sx.freeMarginPct = 30
	if err := b.checkFreeMargin(ctx); err != nil {
		t.Fatalf("healthy checkFreeMargin = %v", err)
	}
	if b.freeMarginWarned {
		t.Fatal("expected warning latch reset above 25%")
	}

	bad := &Bot{cfg: Config{}, logger: slog.Default(), ex: &stubExchange{freeMarginErr: errors.New("boom")}}
	if err := bad.checkFreeMargin(ctx); err == nil {
		t.Fatal("expected margin error to propagate")
	}
}

func TestCancelPartialGrid(t *testing.T) {
	ctx := context.Background()
	b := &Bot{cfg: Config{}, logger: slog.Default(), ex: &stubExchange{}}
	err := b.cancelPartialGrid(ctx, "limit BUY order placement failed", 100, errors.New("boom"))
	if err == nil || !strings.Contains(err.Error(), "partial grid cancelled") {
		t.Fatalf("expected clean cancel message, got %v", err)
	}
	b2 := &Bot{cfg: Config{}, logger: slog.Default(), ex: &stubExchange{cancelAllErr: errors.New("cancel down")}}
	err = b2.cancelPartialGrid(ctx, "limit BUY order placement failed", 100, errors.New("boom"))
	if err == nil || !strings.Contains(err.Error(), "partial grid cancel failed") {
		t.Fatalf("expected cancel failure message, got %v", err)
	}
}

func TestPlaceReduceOnlyTPSell(t *testing.T) {
	ctx := context.Background()
	sx := &stubExchange{}
	b := testBot(sx)
	lvl := GridLevel{Index: 0, BuyPrice: 99, TPPrice: 100}
	placed, _, id, err := b.placeReduceOnlyTPSell(ctx, lvl, 10, 0)
	if err != nil {
		t.Fatalf("placeReduceOnlyTPSell = %v", err)
	}
	if placed.TPPrice != 100 || id == 0 {
		t.Fatalf("unexpected placement: %+v id=%d", placed, id)
	}
	if len(sx.reduceOnly) != 1 || !sx.reduceOnly[0] {
		t.Fatalf("expected reduce-only order, got %#v", sx.reduceOnly)
	}
}

func TestPlaceReduceOnlyTPSellRetriesOnPriceBand(t *testing.T) {
	ctx := context.Background()
	sx := &stubExchange{failSellBelow: 100.5}
	b := testBot(sx)
	lvl := GridLevel{Index: 0, BuyPrice: 99, TPPrice: 100}
	placed, _, _, err := b.placeReduceOnlyTPSell(ctx, lvl, 10, 101)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if placed.TPPrice <= 101 || len(sx.placed) != 1 {
		t.Fatalf("expected lifted retry price, got %+v placed=%#v", placed, sx.placed)
	}
}

func TestPlaceReduceOnlyTPSellNoRetryWithoutFallback(t *testing.T) {
	ctx := context.Background()
	sx := &stubExchange{failSellBelow: 100.5}
	b := testBot(sx)
	lvl := GridLevel{Index: 0, BuyPrice: 99, TPPrice: 100}
	if _, _, _, err := b.placeReduceOnlyTPSell(ctx, lvl, 10, 0); err == nil {
		t.Fatal("expected price-band error without fallback")
	}
	if len(sx.placed) != 0 {
		t.Fatalf("no order should be placed, got %#v", sx.placed)
	}
}

func TestPlaceReduceOnlyTPSellGenericError(t *testing.T) {
	ctx := context.Background()
	b := testBot(&genericPlaceErrorVenue{err: errors.New("network down")})
	lvl := GridLevel{Index: 0, BuyPrice: 99, TPPrice: 100}
	if _, _, _, err := b.placeReduceOnlyTPSell(ctx, lvl, 10, 101); err == nil {
		t.Fatal("expected generic placement error")
	}
}

func TestTrackActiveOrders(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	b.tracked[10] = &TrackedOrder{ClientOrderID: 10, Side: "buy", Price: 1, LevelIndex: 0}

	activeBuys := map[int]Order{
		0: {ClientOrderIndex: 10, Price: "99.5"},
		1: {ClientOrderIndex: 11},
		9: {ClientOrderIndex: 12, Price: "50"},
	}
	activeTPs := map[int][]Order{
		0:  {{ClientOrderIndex: 20, Price: "101.5"}},
		1:  {{ClientOrderIndex: 21}},
		-1: {{ClientOrderIndex: 22, Price: "300"}},
	}
	b.trackActiveOrders(activeBuys, activeTPs, 10)

	if got := b.tracked[10].Price; got != 1 {
		t.Fatalf("already tracked order was overwritten: %v", got)
	}
	if got := b.tracked[11]; got == nil || got.Price != 99 {
		t.Fatalf("fallback buy price wrong: %+v", got)
	}
	if got := b.tracked[12]; got != nil {
		t.Fatal("out-of-range buy level should be skipped")
	}
	if got := b.tracked[20]; got == nil || got.Price != 101.5 || got.Side != "sell" {
		t.Fatalf("active TP not tracked: %+v", got)
	}
	if got := b.tracked[21]; got == nil || got.Side != "sell" {
		t.Fatalf("fallback TP not tracked: %+v", got)
	}
	if got := b.tracked[22]; got != nil {
		t.Fatal("negative TP level should be skipped")
	}
}

func TestOnWSUpdateDryRunIgnored(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	b.cfg.DryRun = true
	b.onWSUpdate(AccountMarketUpdate{Orders: []Order{{ClientOrderIndex: 1, Status: "filled"}}})
	if sx.ordersByHits != 0 || len(sx.placed) != 0 {
		t.Fatal("dry run must not reconcile")
	}
}

func TestOnWSUpdateReconcilesFilledBuy(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	id := EncodeBuyClientOrderID(0)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10, PlacedAt: time.Now()}
	b.onWSUpdate(AccountMarketUpdate{Orders: []Order{{ClientOrderIndex: id, Status: "filled", IsAsk: false}}})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected a TP sell after a filled buy, got %#v", sx.placed)
	}
}

func TestOnWSUpdateTradeDedupeAndAccountFilter(t *testing.T) {
	sx := &stubExchange{ordersBy: map[int64]Order{5: {ClientOrderIndex: 5, Status: "filled", IsAsk: false}}}
	b := testBot(sx)
	b.accountIndex = 7
	id := int64(5)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10, PlacedAt: time.Now()}

	trade := Trade{TradeID: 1, BidClientID: 5, BidAccountID: 7}
	b.onWSUpdate(AccountMarketUpdate{Trades: []Trade{trade}})
	if sx.ordersByHits != 1 {
		t.Fatalf("expected one lookup, got %d", sx.ordersByHits)
	}
	// Duplicate trade id must be ignored.
	b.onWSUpdate(AccountMarketUpdate{Trades: []Trade{trade}})
	if sx.ordersByHits != 1 {
		t.Fatalf("duplicate trade was reprocessed, hits=%d", sx.ordersByHits)
	}
	// Trade that does not involve this account must be ignored.
	b.onWSUpdate(AccountMarketUpdate{Trades: []Trade{{TradeID: 2, BidClientID: 5, BidAccountID: 99}}})
	if sx.ordersByHits != 1 {
		t.Fatalf("foreign trade was processed, hits=%d", sx.ordersByHits)
	}
}

func TestHandleTradeLookupFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	sx := &stubExchange{ordersByErr: errors.New("rpc down")}
	b := testBot(sx)
	b.tracked[5] = &TrackedOrder{ClientOrderID: 5, Side: "buy", Price: 100}
	b.handleTrade(ctx, Trade{TradeID: 9, BidClientID: 5, BidAccountID: 1})
	if len(sx.placed) != 0 {
		t.Fatalf("lookup failure must not place orders, got %#v", sx.placed)
	}
	// No tracked context available path.
	b2 := testBot(&stubExchange{ordersByErr: errors.New("rpc down")})
	b2.handleTrade(ctx, Trade{TradeID: 10})
}

func TestMinMaxHelpers(t *testing.T) {
	if min(1, 2) != 1 || min(2, 1) != 1 {
		t.Fatal("min wrong")
	}
	if max(1, 2) != 2 || max(2, 1) != 2 {
		t.Fatal("max wrong")
	}
	if max64(1, 2) != 2 || max64(2, 1) != 2 {
		t.Fatal("max64 wrong")
	}
	if got := describePrice(1.5); got != "1.50000000" {
		t.Fatalf("describePrice = %q", got)
	}
}
