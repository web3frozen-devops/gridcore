package gridcore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"
)

type stubExchange struct {
	placed        []string
	placedLevels  []int
	placedPrices  []float64
	placedIDs     []int64
	reduceOnly    []bool
	ordersBy      map[int64]Order
	activeOrders  []Order
	marketResp    MarketMeta
	accountResp   AccountResponse
	cancelAllHits int
	failBuyAfter  int
	failSellAfter int
	failSellBelow float64
	accountHits   int
	activeHits    int
	ordersByHits  int
}

func (s *stubExchange) Market(ctx context.Context) (MarketMeta, error) { return s.marketResp, nil }
func (s *stubExchange) Account(ctx context.Context) (AccountResponse, error) {
	s.accountHits++
	return s.accountResp, nil
}
func (s *stubExchange) ActiveOrders(ctx context.Context) ([]Order, error) {
	s.activeHits++
	return s.activeOrders, nil
}
func (s *stubExchange) OrdersByClientIndexes(ctx context.Context, clientIDs []int64) ([]Order, error) {
	s.ordersByHits++
	var out []Order
	for _, id := range clientIDs {
		if ord, ok := s.ordersBy[id]; ok {
			out = append(out, ord)
		}
	}
	return out, nil
}
func (s *stubExchange) CancelAll(ctx context.Context) error {
	s.cancelAllHits++
	return nil
}
func (s *stubExchange) FreeMarginPct(ctx context.Context) (float64, error) { return 0, nil }

func (s *stubExchange) PlaceLimitOrder(ctx context.Context, side string, level GridLevel, sizeAtomic int64, priceDecimals uint8, reduceOnly bool) (SendTxResponse, int64, error) {
	if side == "buy" && s.failBuyAfter > 0 && countSide(s.placed, "buy") >= s.failBuyAfter {
		return SendTxResponse{}, 0, fmt.Errorf("simulated buy placement failure")
	}
	price := level.BuyPrice
	if side == "buy" {
		s.placed = append(s.placed, side)
		s.placedLevels = append(s.placedLevels, level.Index)
		s.reduceOnly = append(s.reduceOnly, reduceOnly)
		clientID := encodeBuyClientOrderID(level.Index)
		s.placedPrices = append(s.placedPrices, price)
		s.placedIDs = append(s.placedIDs, clientID)
		return SendTxResponse{TxHash: "buy-hash"}, clientID, nil
	}
	price = level.TPPrice
	if s.failSellBelow > 0 && price < s.failSellBelow {
		return SendTxResponse{}, 0, fmt.Errorf(`sendTx failed: {"code":21733,"message":"order price flagged as an accidental price"}`)
	}
	if s.failSellAfter > 0 && countSide(s.placed, "sell") >= s.failSellAfter {
		return SendTxResponse{}, 0, fmt.Errorf(`sendTx failed: {"code":21733,"message":"order price flagged as an accidental price"}`)
	}
	s.placed = append(s.placed, side)
	s.placedLevels = append(s.placedLevels, level.Index)
	s.reduceOnly = append(s.reduceOnly, reduceOnly)
	clientID := encodeTPClientOrderID(level.Index)
	s.placedPrices = append(s.placedPrices, price)
	s.placedIDs = append(s.placedIDs, clientID)
	return SendTxResponse{TxHash: "sell-hash"}, clientID, nil
}

func countSide(sides []string, want string) int {
	n := 0
	for _, side := range sides {
		if side == want {
			n++
		}
	}
	return n
}

func TestReconcileExpiredOrderReplacesSameBuyLevel(t *testing.T) {
	b := &Bot{
		logger: slog.Default(),
		grid:   []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}},
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10},
		},
	}
	sx := &stubExchange{}
	b.ex = sx
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: 1, Status: "expired"})
	if len(sx.placed) != 1 || sx.placed[0] != "buy" {
		t.Fatalf("expected buy re-placement, got %#v", sx.placed)
	}
}

func TestReconcileExpiredOrderReplacesSameTPLevel(t *testing.T) {
	b := &Bot{
		logger: slog.Default(),
		grid:   []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}},
		tracked: map[int64]*TrackedOrder{
			2: {ClientOrderID: 2, Side: "sell", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10},
		},
	}
	sx := &stubExchange{}
	b.ex = sx
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: 2, Status: "canceled-expired"})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected sell re-placement, got %#v", sx.placed)
	}
}

func TestPollAndReconcileReplacesExpiredTrackedOrder(t *testing.T) {
	b := &Bot{
		cfg:    Config{OrderSize: 0.05},
		logger: slog.Default(),
		grid:   []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}},
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10},
		},
		market: MarketMeta{MarketID: 32, SizeDecimals: 2},
	}
	sx := &stubExchange{ordersBy: map[int64]Order{
		1: {ClientOrderIndex: 1, Status: "expired"},
	}}
	b.ex = sx
	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sx.placed) != 1 || sx.placed[0] != "buy" {
		t.Fatalf("expected buy re-placement from polling, got %#v", sx.placed)
	}
}

func TestOnWSUpdateReplacesExpiredTrackedOrder(t *testing.T) {
	b := &Bot{
		logger: slog.Default(),
		grid:   []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}},
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "sell", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10},
		},
	}
	sx := &stubExchange{ordersBy: map[int64]Order{}}
	b.ex = sx
	b.onWSUpdate(AccountMarketUpdate{
		Orders: []Order{{ClientOrderIndex: 1, Status: "canceled-expired"}},
	})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected sell re-placement from ws update, got %#v", sx.placed)
	}
}

func TestBootstrapPlacesTPOrdersForInitialPosition(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.05"}},
		},
	}
	b := &Bot{
		cfg: Config{
			DryRun:       false,
			OrderSize:    0.01,
			GridSpacing:  0.2,
			ProfitPct:    0.2,
			NumLevels:    5,
			ExchangeName: "Lighter",
		},
		logger:  slog.Default(),
		ex:      sx,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
	}
	if err := b.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sx.cancelAllHits != 1 {
		t.Fatalf("expected cancel-all on startup, got %d", sx.cancelAllHits)
	}
	if got := len(sx.placed); got != 10 {
		t.Fatalf("expected 10 orders (5 buys + 5 tps), got %d", got)
	}
	for i := 0; i < 5; i++ {
		if sx.placed[i] != "sell" {
			t.Fatalf("expected tp sell order at index %d, got %s", i, sx.placed[i])
		}
		if !sx.reduceOnly[i] {
			t.Fatalf("expected tp sell order at index %d to be reduce-only", i)
		}
	}
	for i := 5; i < 10; i++ {
		if sx.placed[i] != "buy" {
			t.Fatalf("expected buy order at index %d, got %s", i, sx.placed[i])
		}
		if sx.reduceOnly[i] {
			t.Fatalf("expected buy order at index %d to not be reduce-only", i)
		}
	}
}

func TestBootstrapKeepsTPsWhenBuysRunOutOfMargin(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.05"}},
		},
		failBuyAfter: 1,
	}
	b := &Bot{
		cfg: Config{
			DryRun:       false,
			OrderSize:    0.01,
			GridSpacing:  0.2,
			ProfitPct:    0.2,
			NumLevels:    5,
			ExchangeName: "Lighter",
		},
		logger:  slog.Default(),
		ex:      sx,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
	}
	if err := b.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sx.cancelAllHits != 1 {
		t.Fatalf("expected one startup cancel-all, got %d", sx.cancelAllHits)
	}
	if len(sx.placed) < 5 || sx.placed[0] != "sell" {
		t.Fatalf("expected TP sells first, got %#v", sx.placed)
	}
	if countSide(sx.placed, "sell") < 5 {
		t.Fatalf("expected TP coverage to be preserved, got %#v", sx.placed)
	}
}

func TestBootstrapSkipsAccidentalPriceLevels(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.05"}},
		},
		failSellAfter: 2,
	}
	b := &Bot{
		cfg: Config{
			DryRun:       false,
			OrderSize:    0.01,
			GridSpacing:  0.2,
			ProfitPct:    0.2,
			NumLevels:    5,
			ExchangeName: "Lighter",
		},
		logger:  slog.Default(),
		ex:      sx,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
	}
	if err := b.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sx.cancelAllHits != 1 {
		t.Fatalf("expected startup cancel-all once, got %d", sx.cancelAllHits)
	}
	if countSide(sx.placed, "sell") != 2 {
		t.Fatalf("expected partial TP ladder to remain after accidental price rejection, got %#v", sx.placed)
	}
	if countSide(sx.placed, "buy") != 5 {
		t.Fatalf("expected buy ladder to continue after TP rejection, got %#v", sx.placed)
	}
}

func TestReconcileFilledBuyPlacesTPSell(t *testing.T) {
	sx := &stubExchange{}
	b := &Bot{
		logger: slog.Default(),
		grid:   []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}},
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10},
		},
	}
	b.ex = sx
	b.market = MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: 1, IsAsk: false, Status: "filled"})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected TP sell after buy fill, got %#v", sx.placed)
	}
	if len(sx.reduceOnly) != 1 || !sx.reduceOnly[0] {
		t.Fatalf("expected TP sell after buy fill to be reduce-only, got %#v", sx.reduceOnly)
	}
}

func TestReconcileStartupRecoveredTPRestoresImpliedBuyPrice(t *testing.T) {
	grid := buildGrid(1594.209635, 0.5, 0.5, 5)
	recovered := GridLevel{Index: 2, TPPrice: 1610.15, BuyPrice: roundPrice(1610.15/1.005, 2)}
	sx := &stubExchange{}
	b := &Bot{
		cfg:    Config{OrderSize: 0.1, ProfitPct: 0.5},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid:   grid,
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "sell", LevelIndex: recovered.Index, BuyPrice: recovered.BuyPrice, TPPrice: recovered.TPPrice, SizeAtomic: 10},
		},
		market: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:     sx,
	}

	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: 1, IsAsk: true, Status: "filled"})

	if len(sx.placed) != 1 || sx.placed[0] != "buy" {
		t.Fatalf("expected restored buy after recovered TP fill, got %#v", sx.placed)
	}
	if got, want := sx.placedPrices[0], recovered.BuyPrice; math.Abs(got-want) > 0.0000001 {
		t.Fatalf("expected restored buy at implied TP/profit price %.8f, got %.8f", want, got)
	}
	if math.Abs(sx.placedPrices[0]-grid[2].BuyPrice) < 0.0000001 {
		t.Fatalf("restored buy should not use downward grid level price %.8f", grid[2].BuyPrice)
	}
}

func TestPollAndReconcilePlacesMissingTPsForLongPosition(t *testing.T) {
	grid := []GridLevel{
		{Index: 0, BuyPrice: 100, TPPrice: 101},
		{Index: 1, BuyPrice: 99, TPPrice: 99.99},
		{Index: 2, BuyPrice: 98, TPPrice: 98.98},
	}

	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.15"}},
		},
		activeOrders: nil,
	}
	b := &Bot{
		cfg:     Config{OrderSize: 0.05},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid:    grid,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:      sx,
	}

	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(sx.placed) != 3 {
		t.Fatalf("expected 3 missing TP orders, got %d (%#v)", len(sx.placed), sx.placed)
	}
	for i, side := range sx.placed {
		if side != "sell" || !sx.reduceOnly[i] {
			t.Fatalf("expected reduced-only sell at %d, got side=%s reduceOnly=%v", i, side, sx.reduceOnly[i])
		}
		if sx.placedLevels[i] != i {
			t.Fatalf("expected TP level %d, got %d", i, sx.placedLevels[i])
		}
	}
}

func TestPollAndReconcileDoesNotDuplicateActiveTPs(t *testing.T) {
	tpID0 := encodeTPClientOrderID(0)
	tpID1 := encodeTPClientOrderID(1)
	sx := &stubExchange{
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.10"}},
		},
		activeOrders: []Order{
			{ClientOrderIndex: tpID0, MarketIndex: 32, IsAsk: true, ReduceOnly: true, Status: "open"},
			{ClientOrderIndex: tpID1, MarketIndex: 32, IsAsk: true, ReduceOnly: true, Status: "open"},
		},
	}
	b := &Bot{
		cfg:    Config{OrderSize: 0.05},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid: []GridLevel{
			{Index: 0, BuyPrice: 100, TPPrice: 101},
			{Index: 1, BuyPrice: 99, TPPrice: 99.99},
		},
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:      sx,
	}

	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sx.placed) != 0 {
		t.Fatalf("expected no duplicate TP orders, got %#v", sx.placed)
	}
	if len(b.tracked) != 2 {
		t.Fatalf("expected active TPs to be tracked, got %d tracked orders", len(b.tracked))
	}
}

func TestPollAndReconcileSkipsWhenWebSocketConnected(t *testing.T) {
	sx := &stubExchange{
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.10"}},
		},
	}
	b := &Bot{
		cfg:    Config{OrderSize: 0.05},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid: []GridLevel{
			{Index: 0, BuyPrice: 100, TPPrice: 101},
		},
		tracked:     make(map[int64]*TrackedOrder),
		market:      MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:          sx,
		wsConnected: true,
	}

	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sx.accountHits != 0 || sx.activeHits != 0 || sx.ordersByHits != 0 {
		t.Fatalf("expected no REST polling while websocket is connected, got account=%d active=%d ordersBy=%d", sx.accountHits, sx.activeHits, sx.ordersByHits)
	}
}

func TestChunkClientOrderIDs(t *testing.T) {
	ids := make([]int64, 41)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	chunks := chunkClientOrderIDs(ids, 20)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if len(chunks[0]) != 20 || len(chunks[1]) != 20 || len(chunks[2]) != 1 {
		t.Fatalf("unexpected chunk sizes %#v", []int{len(chunks[0]), len(chunks[1]), len(chunks[2])})
	}
}

func TestShutdownCancelsAllOrders(t *testing.T) {
	sx := &stubExchange{}
	b := &Bot{
		cfg:    Config{ExchangeName: "Lighter"},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ex:     sx,
	}
	b.shutdown()
	if sx.cancelAllHits != 1 {
		t.Fatalf("expected shutdown cancel-all once, got %d", sx.cancelAllHits)
	}
}

func TestShutdownIgnoresLateExpiredUpdates(t *testing.T) {
	b := &Bot{
		logger: slog.Default(),
		grid:   []GridLevel{{Index: 0, BuyPrice: 100, TPPrice: 101}},
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10},
		},
		shuttingDown: true,
	}
	sx := &stubExchange{}
	b.ex = sx
	b.reconcileOrderState(context.Background(), Order{ClientOrderIndex: 1, Status: "expired"})
	if len(sx.placed) != 0 {
		t.Fatalf("expected no re-placement during shutdown, got %#v", sx.placed)
	}
}

func TestBootstrapTPCoverageCanExceedNumGridLevels(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.12"}},
		},
	}
	b := &Bot{
		cfg: Config{
			DryRun:       false,
			OrderSize:    0.01,
			GridSpacing:  0.2,
			ProfitPct:    0.2,
			NumLevels:    5,
			ExchangeName: "Lighter",
		},
		logger:  slog.Default(),
		ex:      sx,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
	}
	if err := b.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countSide(sx.placed, "sell"); got != 12 {
		t.Fatalf("expected 12 startup TP sells to match position units, got %d", got)
	}
	if got := countSide(sx.placed, "buy"); got != 5 {
		t.Fatalf("expected 5 startup buys from NUM_GRID_LEVELS, got %d", got)
	}
	if len(sx.placedLevels) < 12 {
		t.Fatalf("expected placed levels to include TP ladder")
	}
	if got := sx.placedLevels[11]; got != 11 {
		t.Fatalf("expected TP overflow level index 11, got %d", got)
	}
}

func TestBootstrapStartupTPsArePostOnlySafe(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.03"}},
		},
	}
	b := &Bot{
		cfg: Config{
			DryRun:       false,
			OrderSize:    0.01,
			GridSpacing:  0.2,
			ProfitPct:    0.2,
			NumLevels:    3,
			ExchangeName: "Lighter",
		},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		ex:      sx,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
	}
	if err := b.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	mark := 100.0
	prev := 0.0
	for i, side := range sx.placed {
		if side != "sell" {
			continue
		}
		if got := sx.placedPrices[i]; got <= mark {
			t.Fatalf("expected startup TP %d to be above mark %.2f, got %.8f", i, mark, got)
		}
		if prev > 0 && sx.placedPrices[i] <= prev {
			t.Fatalf("expected startup safe TP ladder to be increasing, prev=%.8f current=%.8f", prev, sx.placedPrices[i])
		}
		prev = sx.placedPrices[i]
	}
}

func TestPollAndReconcileRecoversTPBeyondConfiguredBuyLevels(t *testing.T) {
	grid := []GridLevel{
		{Index: 0, BuyPrice: 100, TPPrice: 101},
		{Index: 1, BuyPrice: 99, TPPrice: 99.99},
		{Index: 2, BuyPrice: 98, TPPrice: 98.98},
	}
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.25"}},
		},
		activeOrders: nil,
	}
	b := &Bot{
		cfg:     Config{OrderSize: 0.05, GridSpacing: 1, ProfitPct: 1},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid:    grid,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:      sx,
	}
	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sx.placed); got != 5 {
		t.Fatalf("expected 5 recovered TP orders for position units, got %d", got)
	}
	if got := sx.placedLevels[3]; got != 3 {
		t.Fatalf("expected first overflow TP level to be 3, got %d", got)
	}
	if got := sx.placedLevels[4]; got != 4 {
		t.Fatalf("expected second overflow TP level to be 4, got %d", got)
	}
}

func TestBootstrapRetriesAccidentalPriceWithFallbackTP(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.12"}},
		},
		failSellBelow: 99.80,
	}
	b := &Bot{
		cfg: Config{
			DryRun:       false,
			OrderSize:    0.01,
			GridSpacing:  0.2,
			ProfitPct:    0.2,
			NumLevels:    5,
			ExchangeName: "Lighter",
		},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		ex:      sx,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
	}
	if err := b.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countSide(sx.placed, "sell"); got != 12 {
		t.Fatalf("expected fallback to maintain 12 TP sells, got %d", got)
	}
	if got := countSide(sx.placed, "buy"); got != 5 {
		t.Fatalf("expected 5 buy ladder orders, got %d", got)
	}
	seenSellPrices := map[float64]struct{}{}
	seenSellAtomic := map[int64]struct{}{}
	for i, side := range sx.placed {
		if side != "sell" {
			continue
		}
		price := sx.placedPrices[i]
		if _, exists := seenSellPrices[price]; exists {
			t.Fatalf("expected fallback retries to avoid duplicate TP prices, duplicate=%v", price)
		}
		seenSellPrices[price] = struct{}{}
		atomic := priceToAtomic(price, 2)
		if _, exists := seenSellAtomic[atomic]; exists {
			t.Fatalf("expected fallback retries to avoid duplicate atomic TP prices, duplicate_atomic=%d", atomic)
		}
		seenSellAtomic[atomic] = struct{}{}
	}
}

func TestReconcileFilledBuyUsesTrackedTPPriceNotGridLevel(t *testing.T) {
	grid := buildGrid(1608.77, 0.5, 0.5, 20)
	sx := &stubExchange{}
	b := &Bot{
		cfg:    Config{OrderSize: 0.15, ProfitPct: 0.5},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid:   grid,
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "buy", LevelIndex: 1, BuyPrice: 1608.73, TPPrice: 1616.77, SizeAtomic: 15, PlacedAt: time.Now()},
		},
		market: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:     sx,
	}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: 1, IsAsk: false, Status: "filled"})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected TP sell after restored buy fill, got %#v", sx.placed)
	}
	if got, want := sx.placedPrices[0], 1616.77; math.Abs(got-want) > 0.0000001 {
		t.Fatalf("expected TP above buy price at %.2f, got %.8f (grid level TP would be %.8f)", want, got, grid[1].TPPrice)
	}
	if sx.placedPrices[0] <= 1608.73 {
		t.Fatalf("TP %.8f must be above buy price 1608.73", sx.placedPrices[0])
	}
}

func TestReconcileFilledBuyComputesTPWhenTrackedTPInvalid(t *testing.T) {
	sx := &stubExchange{}
	b := &Bot{
		cfg:    Config{OrderSize: 0.15, ProfitPct: 0.5},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid: []GridLevel{
			{Index: 0, BuyPrice: 100, TPPrice: 101},
		},
		tracked: map[int64]*TrackedOrder{
			1: {ClientOrderID: 1, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 90, SizeAtomic: 10, PlacedAt: time.Now()},
		},
		market: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:     sx,
	}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: 1, IsAsk: false, Status: "filled"})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected TP sell after buy fill, got %#v", sx.placed)
	}
	if got, want := sx.placedPrices[0], 100.5; math.Abs(got-want) > 0.0000001 {
		t.Fatalf("expected TP recomputed from buy price as %.2f, got %.8f", want, got)
	}
}

func TestCoverageCountsTPUnitsNotDistinctLevels(t *testing.T) {
	tpID0a := encodeTPClientOrderID(0)
	tpID0b := encodeTPClientOrderID(0)
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.10"}},
		},
		activeOrders: []Order{
			{ClientOrderIndex: tpID0a, MarketIndex: 32, IsAsk: true, ReduceOnly: true, Status: "open", RemainingBaseAmount: "5", Price: "101"},
			{ClientOrderIndex: tpID0b, MarketIndex: 32, IsAsk: true, ReduceOnly: true, Status: "open", RemainingBaseAmount: "5", Price: "101"},
		},
	}
	b := &Bot{
		cfg:    Config{OrderSize: 0.05},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid: []GridLevel{
			{Index: 0, BuyPrice: 100, TPPrice: 101},
		},
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		ex:      sx,
	}
	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sx.placed) != 0 {
		t.Fatalf("expected no duplicate TP orders when units are covered at one level, got %#v", sx.placed)
	}
	if len(b.tracked) != 2 {
		t.Fatalf("expected both active TPs to be tracked, got %d", len(b.tracked))
	}
}

func TestCoverageAnchorsRecoveredTPsAboveMark(t *testing.T) {
	grid := buildGrid(1608.77, 0.5, 0.5, 20)
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "1608.77"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.30"}},
		},
		activeOrders: nil,
	}
	b := &Bot{
		cfg:     Config{OrderSize: 0.15, GridSpacing: 0.5, ProfitPct: 0.5},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid:    grid,
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "1608.77"},
		ex:      sx,
	}
	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sx.placed); got != 2 {
		t.Fatalf("expected 2 recovered TP orders, got %d", got)
	}
	mark := 1608.77
	prev := 0.0
	for i, side := range sx.placed {
		if side != "sell" {
			t.Fatalf("expected sell order at %d, got %s", i, side)
		}
		if sx.placedPrices[i] <= mark {
			t.Fatalf("recovered TP %.8f must be above mark %.2f", sx.placedPrices[i], mark)
		}
		if prev > 0 && sx.placedPrices[i] <= prev {
			t.Fatalf("recovered TP ladder must be increasing, prev=%.8f current=%.8f", prev, sx.placedPrices[i])
		}
		prev = sx.placedPrices[i]
	}
}

func TestCoverageDoesNotReplaceWhileInFlight(t *testing.T) {
	sx := &stubExchange{
		marketResp: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{
			Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.15"}},
		},
		activeOrders: nil,
	}
	b := &Bot{
		cfg:    Config{OrderSize: 0.05, GridSpacing: 1, ProfitPct: 1},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid: []GridLevel{
			{Index: 0, BuyPrice: 100, TPPrice: 101},
		},
		tracked: make(map[int64]*TrackedOrder),
		market:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		ex:      sx,
	}
	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sx.placed); got != 3 {
		t.Fatalf("expected 3 initial recovered TP orders, got %d", got)
	}
	if err := b.pollAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sx.placed); got != 3 {
		t.Fatalf("expected no repeated TP placement while recovery is in flight, got %d total", got)
	}
}

func TestReconcileExpiredTPKeepsActualPrice(t *testing.T) {
	sx := &stubExchange{}
	b := &Bot{
		cfg:    Config{OrderSize: 0.15, ProfitPct: 0.5},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		grid: []GridLevel{
			{Index: 0, BuyPrice: 100, TPPrice: 101},
		},
		tracked: map[int64]*TrackedOrder{
			2: {ClientOrderID: 2, Side: "sell", LevelIndex: 0, BuyPrice: 1608.73, TPPrice: 1616.77, SizeAtomic: 15, PlacedAt: time.Now()},
		},
		market: MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2},
		ex:     sx,
	}
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: 2, Status: "canceled-expired"})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("expected TP sell re-placement, got %#v", sx.placed)
	}
	if got, want := sx.placedPrices[0], 1616.77; math.Abs(got-want) > 0.0000001 {
		t.Fatalf("expected replacement at tracked TP price %.2f, got %.8f", want, got)
	}
}
