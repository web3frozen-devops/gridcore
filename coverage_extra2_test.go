package gridcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var covMarket = MarketMeta{MarketID: 1, MarkPrice: "100000", PriceDecimals: 2, SizeDecimals: 2}

func TestRunPreRunDelegatesToPreview(t *testing.T) {
	sx := &stubExchange{marketResp: covMarket}
	b := New(Config{PreRun: true, ExchangeName: "Test", NumLevels: 2, OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2},
		slog.Default(), sx, nil, nil, 0)
	if err := b.Run(context.Background()); err != nil {
		t.Fatalf("pre-run Run = %v", err)
	}
}

func TestPreviewErrorPaths(t *testing.T) {
	ctx := context.Background()

	marketErr := testBot(&stubExchange{marketErr: errors.New("market down")})
	marketErr.cfg.PreRun = true
	if err := marketErr.Run(ctx); err == nil {
		t.Fatal("expected market error")
	}

	accountErr := testBot(&stubExchange{marketResp: covMarket, accountErr: errors.New("account down")})
	if err := accountErr.preview(ctx); err == nil {
		t.Fatal("expected account error")
	}

	short := testBot(&stubExchange{marketResp: covMarket, accountResp: AccountResponse{
		Positions: []AccountPosition{{MarketID: 1, Sign: -1, Position: "1"}}}})
	if err := short.preview(ctx); err == nil {
		t.Fatal("expected short-position error")
	}
}

func TestPreviewOverflowRecoveryLabel(t *testing.T) {
	sx := &stubExchange{marketResp: covMarket, accountResp: AccountResponse{
		Positions: []AccountPosition{{MarketID: 1, Sign: 1, Position: "0.05"}}}}
	b := testBot(sx)
	if err := b.preview(context.Background()); err != nil {
		t.Fatalf("preview overflow = %v", err)
	}
}

func TestBootstrapErrorPaths(t *testing.T) {
	ctx := context.Background()

	if err := testBot(&stubExchange{marketErr: errors.New("m")}).bootstrap(ctx); err == nil {
		t.Fatal("expected market error")
	}
	if err := testBot(&stubExchange{marketResp: covMarket, accountErr: errors.New("a")}).bootstrap(ctx); err == nil {
		t.Fatal("expected account error")
	}
	short := testBot(&stubExchange{marketResp: covMarket, accountResp: AccountResponse{
		Positions: []AccountPosition{{MarketID: 1, Sign: -1, Position: "1"}}}})
	if err := short.bootstrap(ctx); err == nil {
		t.Fatal("expected short-position error")
	}
	if err := testBot(&stubExchange{marketResp: covMarket, cancelAllErr: errors.New("c")}).bootstrap(ctx); err == nil {
		t.Fatal("expected cancel-all error")
	}
}

type priceBandBuyVenue struct{ stubExchange }

func (v *priceBandBuyVenue) PlaceLimitOrder(ctx context.Context, side string, level GridLevel, sizeAtomic int64, priceDecimals uint8, reduceOnly bool) (SendTxResponse, int64, error) {
	if side == "buy" {
		return SendTxResponse{}, 0, fmt.Errorf(`sendTx failed: {"code":21733,"message":"accidental price"}`)
	}
	return v.stubExchange.PlaceLimitOrder(ctx, side, level, sizeAtomic, priceDecimals, reduceOnly)
}

func TestBootstrapBranchCoverage(t *testing.T) {
	ctx := context.Background()

	// Non-price-band TP placement failure during recovery cancels the partial grid.
	tpFatal := &genericPlaceErrorVenue{err: errors.New("place down")}
	tpFatal.marketResp = covMarket
	tpFatal.accountResp = AccountResponse{Positions: []AccountPosition{{MarketID: 1, Sign: 1, Position: "0.05"}}}
	tpBot := testBot(tpFatal)
	tpBot.cfg.NumLevels = 2
	if err := tpBot.bootstrap(ctx); err == nil {
		t.Fatal("expected fatal TP placement error")
	}
	if tpFatal.cancelAllHits == 0 {
		t.Fatal("expected partial grid cancel")
	}

	// Price-band buy rejection stops the ladder without failing.
	banded := &priceBandBuyVenue{}
	banded.marketResp = covMarket
	bandedBot := testBot(banded)
	bandedBot.cfg.NumLevels = 2
	if err := bandedBot.bootstrap(ctx); err != nil {
		t.Fatalf("price-band buy should break the ladder, got %v", err)
	}
	if len(banded.placed) != 0 {
		t.Fatalf("no buys expected, got %#v", banded.placed)
	}

	// Non-price-band buy failure with no existing position cancels the partial grid.
	buyFatal := &genericPlaceErrorVenue{err: errors.New("place down")}
	buyFatal.marketResp = covMarket
	buyBot := testBot(buyFatal)
	buyBot.cfg.NumLevels = 2
	if err := buyBot.bootstrap(ctx); err == nil {
		t.Fatal("expected fatal buy error")
	}
	if buyFatal.cancelAllHits == 0 {
		t.Fatal("expected partial grid cancel")
	}
}

func TestReconcileExpiredOverflowTPPlaceError(t *testing.T) {
	v := &genericPlaceErrorVenue{err: errors.New("place down")}
	b := testBot(v)
	id := EncodeTPClientOrderID(5)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "sell", LevelIndex: 5, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: id, Status: "expired"})
	b.mu.Lock()
	state, exists := b.tracked[id]
	b.mu.Unlock()
	if !exists || state.Processing {
		t.Fatal("failed overflow TP replacement should stay tracked and unclaimed")
	}
}

func TestNextClientOrderSuffixRandFallback(t *testing.T) {
	orig := randInt
	randInt = func(io.Reader, *big.Int) (*big.Int, error) { return nil, errors.New("entropy down") }
	defer func() { randInt = orig }()
	if got := nextClientOrderSuffix(); got < 0 || got >= clientOrderLevelRange {
		t.Fatalf("fallback suffix out of range: %d", got)
	}
}

func TestStartupRecoveryLevelEdges(t *testing.T) {
	b := testBot(&stubExchange{})
	if lvl, _ := b.startupRecoveryLevel(-1, 100, 0); lvl.Index != 0 {
		t.Fatalf("negative level should clamp, got %d", lvl.Index)
	}

	empty := &Bot{cfg: Config{GridSpacing: 0.2, ProfitPct: 0.2}, logger: slog.Default(), market: covMarket}
	if lvl, _ := empty.startupRecoveryLevel(3, 100, 0); lvl.Index != 3 {
		t.Fatalf("empty grid level = %d", lvl.Index)
	}

	zeroTop := testBot(&stubExchange{})
	zeroTop.grid = []GridLevel{{Index: 0, BuyPrice: 0, TPPrice: 0}}
	if _, _ = zeroTop.startupRecoveryLevel(0, 0, 0); false {
	}

	zeroStep := testBot(&stubExchange{})
	zeroStep.cfg.GridSpacing = -100
	zeroStep.startupRecoveryLevel(0, 0, 0)
}

func TestShutdownCancelAllError(t *testing.T) {
	sx := &stubExchange{cancelAllErr: errors.New("shutdown cancel down")}
	b := testBot(sx)
	b.cfg.DryRun = false
	b.shutdown()
	if sx.cancelAllHits != 1 {
		t.Fatalf("expected one cancel attempt, got %d", sx.cancelAllHits)
	}
}

func TestReleaseTrackedOrderBranches(t *testing.T) {
	b := testBot(&stubExchange{})
	b.releaseTrackedOrder(999, true)

	b.tracked[1] = &TrackedOrder{ClientOrderID: 1, Processing: true}
	b.releaseTrackedOrder(1, true)
	if _, ok := b.tracked[1]; ok {
		t.Fatal("settled order should be deleted")
	}

	b.tracked[2] = &TrackedOrder{ClientOrderID: 2, Processing: true}
	b.releaseTrackedOrder(2, false)
	if b.tracked[2].Processing {
		t.Fatal("unsettled order should be unclaimed")
	}
}

func TestHandleTradeCollectsBothIDs(t *testing.T) {
	sx := &stubExchange{ordersBy: map[int64]Order{
		3: {ClientOrderIndex: 3, Status: "open"},
		4: {ClientOrderIndex: 4, Status: "open"},
	}}
	b := testBot(sx)
	b.tracked[3] = &TrackedOrder{ClientOrderID: 3, Side: "buy", Price: 100}
	b.tracked[4] = &TrackedOrder{ClientOrderID: 4, Side: "sell", Price: 101}
	b.handleTrade(context.Background(), Trade{TradeID: 1, AskClientID: 3, BidClientID: 4})
	if sx.ordersByHits != 1 {
		t.Fatalf("lookup hits = %d", sx.ordersByHits)
	}
	// Same id on both sides must be collected once and exercise that branch too.
	b.handleTrade(context.Background(), Trade{TradeID: 2, AskClientID: 4, BidClientID: 4})
}

func TestPollAndReconcileLookupError(t *testing.T) {
	sx := &stubExchange{ordersByErr: errors.New("rpc down")}
	b := testBot(sx)
	b.tracked[1] = &TrackedOrder{ClientOrderID: 1, Side: "buy", PlacedAt: time.Now()}
	if err := b.pollAndReconcile(context.Background()); err == nil {
		t.Fatal("expected lookup error")
	}
	b.mu.Lock()
	armed := !b.pollCooldownUntil.IsZero()
	b.mu.Unlock()
	if !armed {
		t.Fatal("expected poll cooldown to be armed")
	}
}

func TestRunLogsPollReconcileError(t *testing.T) {
	sx := &stubExchange{marketResp: covMarket, ordersByErr: errors.New("rpc down")}
	b := New(Config{ExchangeName: "Test", Symbol: "BTC-USDT-PERP", OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2, NumLevels: 2},
		slog.Default(), sx, nil, nil, 0)
	b.pollEvery = 5 * time.Millisecond
	b.marginEvery = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := b.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestReconcileExpiredOrderAlreadyClaimed(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	b.tracked[1] = &TrackedOrder{ClientOrderID: 1, Side: "buy", LevelIndex: 0, Processing: true}
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: 1, Status: "expired"})
	if len(sx.placed) != 0 {
		t.Fatalf("claimed order should be skipped, got %#v", sx.placed)
	}
}

func TestReconcilePositionCoverageEdges(t *testing.T) {
	ctx := context.Background()

	empty := testBot(&stubExchange{})
	empty.grid = nil
	if err := empty.reconcilePositionCoverage(ctx); err != nil {
		t.Fatalf("empty grid = %v", err)
	}

	cooling := testBot(&stubExchange{})
	cooling.nextCoveragePlaceAt = time.Now().Add(time.Minute)
	if err := cooling.reconcilePositionCoverage(ctx); err != nil {
		t.Fatalf("cooldown = %v", err)
	}

	if err := testBot(&stubExchange{accountErr: errors.New("acct")}).reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected account error")
	}

	short := testBot(&stubExchange{accountResp: AccountResponse{
		Positions: []AccountPosition{{MarketID: 1, Sign: -1, Position: "1"}}}})
	if err := short.reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected short-position error")
	}

	if err := testBot(&stubExchange{activeErr: errors.New("active")}).reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected active-orders error")
	}

	badSize := testBot(&stubExchange{})
	badSize.market.SizeDecimals = 0
	if err := badSize.reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected invalid size error")
	}

	marketErr := testBot(&stubExchange{marketErr: errors.New("market"), accountResp: AccountResponse{
		Positions: []AccountPosition{{MarketID: 1, Sign: 1, Position: "0.05"}}}})
	if err := marketErr.reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected market error")
	}

	badMark := testBot(&stubExchange{marketResp: MarketMeta{MarketID: 1, MarkPrice: "nope", PriceDecimals: 2, SizeDecimals: 2},
		accountResp: AccountResponse{Positions: []AccountPosition{{MarketID: 1, Sign: 1, Position: "0.05"}}}})
	if err := badMark.reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected invalid mark price error")
	}

	placeErr := &genericPlaceErrorVenue{err: errors.New("place down")}
	placeErr.marketResp = covMarket
	placeErr.accountResp = AccountResponse{Positions: []AccountPosition{{MarketID: 1, Sign: 1, Position: "0.05"}}}
	if err := testBot(placeErr).reconcilePositionCoverage(ctx); err == nil {
		t.Fatal("expected coverage placement error")
	}
}

func TestInFlightAndPruneWindowBranches(t *testing.T) {
	b := testBot(&stubExchange{})
	b.tracked[1] = &TrackedOrder{ClientOrderID: 1, Side: "sell", PlacedAt: time.Now().Add(-2 * inFlightTPWindow)}
	b.tracked[2] = &TrackedOrder{ClientOrderID: 2, Side: "buy", PlacedAt: time.Now()}
	b.tracked[3] = &TrackedOrder{ClientOrderID: 3, Side: "sell", PlacedAt: time.Now()}
	b.tracked[4] = &TrackedOrder{ClientOrderID: 4, Side: "sell", PlacedAt: time.Now()}
	active := map[int64]struct{}{3: {}}

	if n := b.inFlightTPUnits(active, inFlightTPWindow); n != 1 {
		t.Fatalf("in-flight units = %d, want 1", n)
	}
	b.pruneStaleTrackedTPs(active, inFlightTPWindow)
	if _, ok := b.tracked[1]; ok {
		t.Fatal("stale TP should be pruned")
	}
	if _, ok := b.tracked[2]; !ok {
		t.Fatal("buy must be kept")
	}
	if _, ok := b.tracked[3]; !ok {
		t.Fatal("active TP must be kept")
	}
	if _, ok := b.tracked[4]; !ok {
		t.Fatal("recent in-flight TP must be kept")
	}
}

func TestPlaceReduceOnlyTPSellRetryFails(t *testing.T) {
	sx := &stubExchange{failSellBelow: 1e18}
	b := testBot(sx)
	lvl := GridLevel{Index: 0, BuyPrice: 99, TPPrice: 100}
	if _, _, _, err := b.placeReduceOnlyTPSell(context.Background(), lvl, 10, 50); err == nil {
		t.Fatal("expected retry to fail")
	}
}

func TestTrackActiveOrdersZeroProfitStep(t *testing.T) {
	b := testBot(&stubExchange{})
	b.cfg.ProfitPct = -100
	b.trackActiveOrders(map[int]Order{0: {ClientOrderIndex: 111, Price: "100"}}, nil, 10)
	if b.tracked[111] == nil {
		t.Fatal("expected active buy tracked")
	}
}

func TestGridLevelForIndexEdges(t *testing.T) {
	b := testBot(&stubExchange{})
	b.grid = nil
	if lvl := b.gridLevelForIndex(3); lvl.Index != 3 {
		t.Fatalf("empty grid index = %d", lvl.Index)
	}
	b.grid = []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}}
	b.cfg.GridSpacing = 100
	if lvl := b.gridLevelForIndex(5); lvl.BuyPrice != 100 {
		t.Fatalf("zero-step fallback buy = %v", lvl.BuyPrice)
	}
}

func TestChunkClientOrderIDsZeroSize(t *testing.T) {
	got := chunkClientOrderIDs([]int64{1, 2, 3}, 0)
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("zero-size chunk = %#v", got)
	}
	if empty := chunkClientOrderIDs(nil, 0); len(empty) != 0 {
		t.Fatalf("empty ids chunk = %#v", empty)
	}
}

func TestDecodeLegacyClientOrderIDs(t *testing.T) {
	if lvl, side, ok := DecodeClientOrderID(legacyBuyClientOrderBase + 3); !ok || side != "buy" || lvl != 3 {
		t.Fatalf("legacy buy decode = (%d,%q,%v)", lvl, side, ok)
	}
	if lvl, side, ok := DecodeClientOrderID(legacyTPClientOrderBase + 4); !ok || side != "tp" || lvl != 4 {
		t.Fatalf("legacy tp decode = (%d,%q,%v)", lvl, side, ok)
	}
}

func TestPositionUnitsForMarketBranches(t *testing.T) {
	if got, err := positionUnitsForMarket([]AccountPosition{{MarketID: 9, Sign: 1, Position: "1"}}, 1, 0.01); err != nil || got != 0 {
		t.Fatalf("other market = (%d,%v)", got, err)
	}
	if _, err := positionUnitsForMarket([]AccountPosition{{MarketID: 1, Sign: -1, Position: "1"}}, 1, 0.01); err == nil {
		t.Fatal("expected short-position error")
	}
}

func TestOrderRemainingAtomicParseError(t *testing.T) {
	if got := orderRemainingAtomic(Order{RemainingBaseAmount: "abc"}); got != 0 {
		t.Fatalf("parse error = %d", got)
	}
	if got := orderRemainingAtomic(Order{RemainingBaseAmount: "  "}); got != 0 {
		t.Fatalf("blank = %d", got)
	}
}

func TestClassifyActiveGridOrdersSkips(t *testing.T) {
	buyID := EncodeBuyClientOrderID(0)
	tpID := EncodeTPClientOrderID(0)
	orders := []Order{
		{MarketID: 9, ClientOrderIndex: buyID, Status: "open"},
		{MarketID: 1, ClientOrderIndex: 0, Status: "open"},
		{MarketID: 1, ClientOrderIndex: 1, Status: "filled"},
		{MarketID: 1, ClientOrderIndex: buyID, Status: "open", IsAsk: true},
		{MarketID: 1, ClientOrderIndex: tpID, Status: "open", IsAsk: true, ReduceOnly: false},
		{MarketID: 1, ClientOrderIndex: buyID, Status: "open", IsAsk: false},
		{MarketID: 1, ClientOrderIndex: tpID, Status: "open", IsAsk: true, ReduceOnly: true},
	}
	buys, tps := classifyActiveGridOrders(orders, 1)
	if len(buys) != 1 || len(tps) != 1 {
		t.Fatalf("classify = %d buys, %d tps", len(buys), len(tps))
	}
}

func TestPositionUnitsErrors(t *testing.T) {
	if positionUnits("abc", 0.01) != 0 {
		t.Fatal("bad position should be 0")
	}
	if positionUnits("1", 0) != 0 {
		t.Fatal("zero order size should be 0")
	}
}

func TestPositionListUnmarshalJSONBranches(t *testing.T) {
	var p PositionList
	if err := p.UnmarshalJSON([]byte("null")); err != nil || p != nil {
		t.Fatalf("null = %v %#v", err, p)
	}
	if err := p.UnmarshalJSON([]byte("   ")); err != nil || p != nil {
		t.Fatalf("empty = %v %#v", err, p)
	}
	if err := p.UnmarshalJSON([]byte("[]")); err != nil || len(p) != 0 {
		t.Fatalf("empty array = %v %#v", err, p)
	}
	if err := p.UnmarshalJSON([]byte(`{"market_id":1,"sign":1,"position":"0.05"}`)); err != nil || len(p) != 1 {
		t.Fatalf("object = %v %#v", err, p)
	}
	if err := p.UnmarshalJSON([]byte("[1,2]")); err == nil {
		t.Fatal("expected array unmarshal error")
	}
	if err := p.UnmarshalJSON([]byte(`{"market_id":"x"}`)); err == nil {
		t.Fatal("expected object unmarshal error")
	}
	if err := p.UnmarshalJSON([]byte("123")); err == nil {
		t.Fatal("expected invalid-payload error")
	}
}

func TestLoadDotEnvNonNotExistError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(filepath.Join(file, "child")); err == nil {
		t.Fatal("expected ENOTDIR error")
	}
}
