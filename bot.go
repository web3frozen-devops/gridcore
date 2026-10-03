package gridcore

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
)

type Bot struct {
	cfg      Config
	logger   *slog.Logger
	ex       Venue
	ws       Streamer
	telegram *Telegram

	mu                  sync.Mutex
	accountIndex        int64
	market              MarketMeta
	grid                []GridLevel
	tracked             map[int64]*TrackedOrder
	seenTrades          map[int64]struct{}
	freeMarginWarned    bool
	shuttingDown        bool
	wsConnected         bool
	pollCooldownUntil   time.Time
	nextCoveragePlaceAt time.Time

	// Optional overrides for the reconcile/margin loop intervals. Zero means
	// use the configured values.
	pollEvery   time.Duration
	marginEvery time.Duration
}

// pollInterval is how often the engine reconciles tracked orders by REST.
func (b *Bot) pollInterval() time.Duration {
	if b.pollEvery > 0 {
		return b.pollEvery
	}
	return time.Duration(max(2, b.cfg.PollSeconds)) * time.Second
}

// marginInterval is how often the engine checks free margin.
func (b *Bot) marginInterval() time.Duration {
	if b.marginEvery > 0 {
		return b.marginEvery
	}
	return time.Duration(max(5, b.cfg.MarginSeconds)) * time.Second
}

const (
	inFlightTPWindow      = 90 * time.Second
	coveragePlaceCooldown = 60 * time.Second
)

type trackedOrderSnapshot struct {
	LevelIndex int
	SizeAtomic int64
	BuyPrice   float64
	TPPrice    float64
	Side       string
}

// New builds a grid engine for a single venue. streamer and telegram may be
// nil. accountIndex identifies this process's account so the engine can ignore
// trades that do not involve it.
func New(cfg Config, logger *slog.Logger, venue Venue, streamer Streamer, telegram *Telegram, accountIndex int64) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{
		cfg:          cfg,
		logger:       logger,
		ex:           venue,
		ws:           streamer,
		telegram:     telegram,
		accountIndex: accountIndex,
		tracked:      make(map[int64]*TrackedOrder),
		seenTrades:   make(map[int64]struct{}),
	}
}

func (b *Bot) Run(ctx context.Context) error {
	if b.cfg.PreRun {
		return b.preview(ctx)
	}
	startMsg := fmt.Sprintf("Bot started on %s Perpetual - Grid bot initialized", b.cfg.ExchangeName)
	b.logger.Info(startMsg, "exchange", b.cfg.ExchangeName, "network", b.cfg.Network, "symbol", b.cfg.Symbol, "dry_run", b.cfg.DryRun)
	_ = b.telegram.Send(ctx, startMsg)
	defer b.shutdown()

	if err := b.bootstrap(ctx); err != nil {
		return err
	}

	pollTicker := time.NewTicker(b.pollInterval())
	marginTicker := time.NewTicker(b.marginInterval())
	defer pollTicker.Stop()
	defer marginTicker.Stop()

	if b.ws != nil {
		go func() {
			_ = b.ws.Run(ctx, b.market.MarketID, b.setWSConnected, b.onWSUpdate)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollTicker.C:
			if err := b.pollAndReconcile(ctx); err != nil {
				b.logger.Error("poll reconcile failed", "error", err)
			}
		case <-marginTicker.C:
			_ = b.checkFreeMargin(ctx)
		}
	}
}

func (b *Bot) preview(ctx context.Context) error {
	market, err := b.ex.Market(ctx)
	if err != nil {
		return err
	}
	b.market = market
	grid := buildGrid(parsePriceOrDryRun(b.cfg, market), b.cfg.GridSpacing, b.cfg.ProfitPct, b.cfg.NumLevels)
	sortGridByLevel(grid)
	b.grid = grid
	acct, err := b.ex.Account(ctx)
	if err != nil {
		return err
	}
	posUnits, err := positionUnitsForMarket(acct.Positions, market.MarketID, b.cfg.OrderSize)
	if err != nil {
		return err
	}
	b.logger.Info("preview grid computation", "market_id", market.MarketID, "mark_price", market.MarkPrice, "position_units", posUnits, "order_size", b.cfg.OrderSize, "levels", len(grid))
	markPrice, _ := parseFloat(market.MarkPrice)
	prevAtomic := int64(0)
	for _, lvl := range grid {
		b.logger.Info("preview limit BUY order", "price", lvl.BuyPrice, "size", b.cfg.OrderSize, "level", lvl.Index)
	}
	for i := 0; i < posUnits; i++ {
		var lvl GridLevel
		lvl, prevAtomic = b.startupRecoveryLevel(i, markPrice, prevAtomic)
		label := "preview Reduced-Only limit TP SELL"
		if i >= len(grid) {
			label = "preview overflow TP recovery level"
		}
		b.logger.Info(label, "price", lvl.TPPrice, "size", b.cfg.OrderSize, "level", lvl.Index)
	}
	return nil
}

func (b *Bot) bootstrap(ctx context.Context) error {
	market, err := b.ex.Market(ctx)
	if err != nil {
		return err
	}
	b.market = market
	grid := buildGrid(parsePriceOrDryRun(b.cfg, market), b.cfg.GridSpacing, b.cfg.ProfitPct, b.cfg.NumLevels)
	sortGridByLevel(grid)
	b.grid = grid
	b.logger.Info("startup grid computation", "market_id", market.MarketID, "mark_price", market.MarkPrice, "levels", len(grid))
	if b.cfg.DryRun {
		for _, lvl := range grid {
			b.logger.Info("dry-run planned limit BUY order", "price", lvl.BuyPrice, "size", b.cfg.OrderSize)
			b.logger.Info("dry-run planned Reduced-Only limit TP SELL", "price", lvl.TPPrice, "size", b.cfg.OrderSize)
		}
		return nil
	}
	acct, err := b.ex.Account(ctx)
	if err != nil {
		return err
	}
	posUnits, err := positionUnitsForMarket(acct.Positions, market.MarketID, b.cfg.OrderSize)
	if err != nil {
		return err
	}
	b.logger.Info("position recovery", "market_id", market.MarketID, "position_units", posUnits, "order_size", b.cfg.OrderSize)
	if err := b.ex.CancelAll(ctx); err != nil {
		return err
	}
	sizeAtomic := sizeToAtomic(b.cfg.OrderSize, market.SizeDecimals)
	if sizeAtomic <= 0 {
		return fmt.Errorf("invalid size atomic amount")
	}
	if posUnits > 0 {
		markPrice, _ := parseFloat(market.MarkPrice)
		prevAtomic := int64(0)
		for i := 0; i < posUnits; i++ {
			var lvl GridLevel
			lvl, prevAtomic = b.startupRecoveryLevel(i, markPrice, prevAtomic)
			placedLvl, resp, clientID, err := b.placeReduceOnlyTPSell(ctx, lvl, sizeAtomic, 0)
			if err != nil {
				if b.IsPriceBandError(err) {
					b.logger.Warn("Reduced-Only limit TP SELL rejected by price band; continuing startup TP recovery", "price", lvl.TPPrice, "level", lvl.Index, "error", err)
					continue
				}
				return b.cancelPartialGrid(ctx, "Reduced-Only limit TP SELL placement failed", lvl.TPPrice, err)
			}
			b.logger.Info("Placing Reduced-Only limit TP SELL", "price", placedLvl.TPPrice, "size", b.cfg.OrderSize, "client_order_index", clientID, "tx_hash", resp.TxHash)
			b.trackOrder(placedLvl, "sell", placedLvl.TPPrice, sizeAtomic, clientID)
		}
	}
	for _, lvl := range grid {
		resp, clientID, err := b.ex.PlaceLimitOrder(ctx, "buy", lvl, sizeAtomic, market.PriceDecimals, false)
		if err != nil {
			if b.IsPriceBandError(err) {
				b.logger.Warn("limit BUY rejected by price band; skipping deeper buy levels", "price", lvl.BuyPrice, "error", err)
				break
			}
			if posUnits > 0 {
				b.logger.Error("startup buy ladder incomplete; preserving existing TP coverage", "price", lvl.BuyPrice, "error", err)
				return nil
			}
			return b.cancelPartialGrid(ctx, "limit BUY order placement failed", lvl.BuyPrice, err)
		}
		b.logger.Info("Placing limit BUY order", "price", lvl.BuyPrice, "size", b.cfg.OrderSize, "client_order_index", clientID, "tx_hash", resp.TxHash)
		b.trackOrder(lvl, "buy", lvl.BuyPrice, sizeAtomic, clientID)
	}
	return nil
}

func (b *Bot) startupRecoveryLevel(level int, markPrice float64, prevAtomic int64) (GridLevel, int64) {
	if level < 0 {
		level = 0
	}
	if len(b.grid) == 0 {
		return GridLevel{Index: level}, prevAtomic
	}
	topBuy := b.grid[0].BuyPrice
	if topBuy <= 0 {
		topBuy = b.gridLevelForIndex(0).BuyPrice
	}
	if markPrice > 0 {
		topBuy = markPrice * (1 - b.cfg.GridSpacing/100)
	}
	baseTP := topBuy * (1 + b.cfg.ProfitPct/100)
	step := 1 + b.cfg.GridSpacing/100
	if step <= 0 {
		step = 1
	}
	price := baseTP * math.Pow(step, float64(level))
	atomic := priceToAtomic(price, b.market.PriceDecimals)
	minAtomic := priceToAtomic(markPrice, b.market.PriceDecimals) + 1
	if minAtomic > 0 && atomic < minAtomic {
		atomic = minAtomic
	}
	if atomic <= prevAtomic {
		atomic = prevAtomic + 1
	}
	lvl := b.gridLevelForIndex(level)
	lvl.TPPrice = atomicToPrice(atomic, b.market.PriceDecimals)
	profitStep := 1 + b.cfg.ProfitPct/100
	if profitStep > 0 {
		lvl.BuyPrice = roundPrice(lvl.TPPrice/profitStep, b.market.PriceDecimals)
	}
	return lvl, atomic
}

func (b *Bot) cancelPartialGrid(ctx context.Context, action string, price float64, cause error) error {
	b.logger.Error("startup order placement failed; cancelling partial grid", "action", action, "price", price, "error", cause)
	if err := b.ex.CancelAll(ctx); err != nil {
		b.logger.Error("partial grid cancel failed", "price", price, "error", err)
		return fmt.Errorf("%s at price %s failed: %w; partial grid cancel failed: %v", action, describePrice(price), cause, err)
	}
	b.logger.Info("partial grid cancelled after startup order placement failure", "price", price)
	return fmt.Errorf("%s at price %s failed: %w; partial grid cancelled", action, describePrice(price), cause)
}

func isAccidentalPriceError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "accidental price") || strings.Contains(msg, `"code":21733`)
}

func (b *Bot) shutdown() {
	b.mu.Lock()
	b.shuttingDown = true
	b.mu.Unlock()
	if !b.cfg.DryRun {
		if err := b.ex.CancelAll(context.Background()); err != nil {
			b.logger.Error("shutdown cancel all failed", "error", err)
		} else {
			b.logger.Info("shutdown cancel all orders", "market_id", b.market.MarketID, "symbol", b.cfg.Symbol)
		}
	}
	stopMsg := fmt.Sprintf("Bot stopped on %s Perpetual - Shutting down", b.cfg.ExchangeName)
	b.logger.Info(stopMsg, "exchange", b.cfg.ExchangeName, "network", b.cfg.Network)
	_ = b.telegram.Send(context.Background(), stopMsg)
}

func parsePriceOrDryRun(cfg Config, market MarketMeta) float64 {
	if cfg.DryRun {
		return cfg.DryRunMarkPrice
	}
	f, err := parseFloat(market.MarkPrice)
	if err != nil || f <= 0 {
		return 0
	}
	return f
}

func (b *Bot) trackOrder(lvl GridLevel, side string, price float64, sizeAtomic int64, clientID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tracked[clientID] = &TrackedOrder{
		ClientOrderID: clientID,
		MarketID:      b.market.MarketID,
		Side:          side,
		Price:         price,
		SizeAtomic:    sizeAtomic,
		LevelIndex:    lvl.Index,
		TPPrice:       lvl.TPPrice,
		BuyPrice:      lvl.BuyPrice,
		PriceDecimals: b.market.PriceDecimals,
		SizeDecimals:  b.market.SizeDecimals,
		PlacedAt:      time.Now(),
	}
}

func (b *Bot) claimTrackedOrder(clientOrderID int64) (trackedOrderSnapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.tracked[clientOrderID]
	if !ok || state.Processing {
		return trackedOrderSnapshot{}, false
	}
	state.Processing = true
	return trackedOrderSnapshot{
		LevelIndex: state.LevelIndex,
		SizeAtomic: state.SizeAtomic,
		BuyPrice:   state.BuyPrice,
		TPPrice:    state.TPPrice,
		Side:       state.Side,
	}, true
}

func (b *Bot) releaseTrackedOrder(clientOrderID int64, settled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.tracked[clientOrderID]
	if !ok {
		return
	}
	if settled {
		delete(b.tracked, clientOrderID)
		return
	}
	state.Processing = false
}

func (b *Bot) onWSUpdate(update AccountMarketUpdate) {
	if b.cfg.DryRun {
		return
	}
	for _, ord := range update.Orders {
		b.reconcileOrderState(context.Background(), ord)
	}
	for _, t := range update.Trades {
		if _, seen := b.seenTrades[t.TradeID]; seen {
			continue
		}
		b.seenTrades[t.TradeID] = struct{}{}
		if t.BidAccountID == b.accountIndex || t.AskAccountID == b.accountIndex {
			b.handleTrade(context.Background(), t)
		}
	}
}

func (b *Bot) setWSConnected(connected bool) {
	b.mu.Lock()
	b.wsConnected = connected
	b.mu.Unlock()
}

func (b *Bot) handleTrade(ctx context.Context, trade Trade) {
	var ids []int64
	if trade.AskClientID > 0 {
		ids = append(ids, trade.AskClientID)
	}
	if trade.BidClientID > 0 && trade.BidClientID != trade.AskClientID {
		ids = append(ids, trade.BidClientID)
	}
	orders, err := b.ex.OrdersByClientIndexes(ctx, ids)
	if err != nil {
		price, hasPrice, orderPrices := b.trackedOrderPriceContext(ids)
		args := []any{"error", err, "client_order_indexes", ids, "order_prices", orderPrices}
		if hasPrice {
			args = append(args, "price", price)
		}
		b.logger.Error("order lookup failed", args...)
		return
	}
	for _, ord := range orders {
		if ord.Status == "filled" {
			b.reconcileFilledOrder(ctx, ord)
		}
	}
}

func (b *Bot) reconcileFilledOrder(ctx context.Context, ord Order) {
	snap, ok := b.claimTrackedOrder(ord.ClientOrderIndex)
	if !ok {
		return
	}
	if snap.LevelIndex < 0 {
		b.releaseTrackedOrder(ord.ClientOrderIndex, false)
		b.logger.Error("tracked order level is outside grid", "price", snap.BuyPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		return
	}
	if ord.IsAsk && snap.LevelIndex >= len(b.grid) {
		b.logger.Info("TP sell filled for recovered overflow unit", "price", snap.TPPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		b.releaseTrackedOrder(ord.ClientOrderIndex, true)
		return
	}
	if snap.LevelIndex >= len(b.grid) {
		b.releaseTrackedOrder(ord.ClientOrderIndex, false)
		b.logger.Error("tracked order level is outside grid", "price", snap.BuyPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		return
	}

	if ord.IsAsk {
		b.logger.Info("TP sell filled", "price", snap.TPPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		lvl := GridLevel{Index: snap.LevelIndex, BuyPrice: snap.BuyPrice, TPPrice: snap.TPPrice}
		_, clientID, err := b.ex.PlaceLimitOrder(ctx, "buy", lvl, snap.SizeAtomic, b.market.PriceDecimals, false)
		if err != nil {
			b.releaseTrackedOrder(ord.ClientOrderIndex, false)
			b.logger.Error("restore buy failed", "price", snap.BuyPrice, "error", err)
			return
		}
		b.logger.Info("TP sell filled → Restoring buy at original grid level", "price", snap.BuyPrice, "size", b.cfg.OrderSize, "client_order_index", clientID)
		b.trackOrder(lvl, "buy", snap.BuyPrice, snap.SizeAtomic, clientID)
		b.releaseTrackedOrder(ord.ClientOrderIndex, true)
		return
	}
	tpPrice := snap.TPPrice
	if tpPrice <= snap.BuyPrice || tpPrice <= 0 {
		tpPrice = roundPrice(snap.BuyPrice*(1+b.cfg.ProfitPct/100), b.market.PriceDecimals)
	}
	lvl := GridLevel{Index: snap.LevelIndex, BuyPrice: snap.BuyPrice, TPPrice: tpPrice}
	b.logger.Info("Buy filled → Placing TP sell", "price", snap.BuyPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
	placedLvl, _, clientID, err := b.placeReduceOnlyTPSell(ctx, lvl, snap.SizeAtomic, 0)
	if err != nil {
		b.releaseTrackedOrder(ord.ClientOrderIndex, false)
		b.logger.Error("tp placement failed", "price", tpPrice, "error", err)
		return
	}
	b.logger.Info("Buy filled at limit price", "price", snap.BuyPrice, "tp_price", placedLvl.TPPrice, "size", b.cfg.OrderSize, "client_order_index", clientID)
	b.trackOrder(placedLvl, "sell", placedLvl.TPPrice, snap.SizeAtomic, clientID)
	b.releaseTrackedOrder(ord.ClientOrderIndex, true)
}

func (b *Bot) pollAndReconcile(ctx context.Context) error {
	if b.cfg.DryRun {
		return nil
	}
	b.mu.Lock()
	wsConnected := b.wsConnected
	pollCooldownUntil := b.pollCooldownUntil
	b.mu.Unlock()
	if wsConnected {
		return nil
	}
	if !pollCooldownUntil.IsZero() && time.Now().Before(pollCooldownUntil) {
		return nil
	}
	ids := b.trackedClientOrderIDs()
	if len(ids) > 0 {
		for _, chunk := range chunkClientOrderIDs(ids, 20) {
			orders, err := b.ex.OrdersByClientIndexes(ctx, chunk)
			if err != nil {
				b.setPollCooldown(err)
				return err
			}
			for _, ord := range orders {
				b.reconcileOrderState(ctx, ord)
			}
		}
	}
	return b.reconcilePositionCoverage(ctx)
}

func (b *Bot) setPollCooldown(err error) {
	if err == nil {
		return
	}
	delay := b.pollInterval()
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	if strings.Contains(strings.ToLower(err.Error()), "429") {
		delay = 30 * time.Second
	}
	b.mu.Lock()
	next := time.Now().Add(delay)
	if next.After(b.pollCooldownUntil) {
		b.pollCooldownUntil = next
	}
	b.mu.Unlock()
}

func (b *Bot) trackedClientOrderIDs() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]int64, 0, len(b.tracked))
	for id := range b.tracked {
		ids = append(ids, id)
	}
	return ids
}

func (b *Bot) trackedOrderPriceContext(clientIDs []int64) (float64, bool, []map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]map[string]any, 0, len(clientIDs))
	var firstPrice float64
	hasPrice := false
	for _, id := range clientIDs {
		state, ok := b.tracked[id]
		if !ok {
			continue
		}
		if !hasPrice {
			firstPrice = state.Price
			hasPrice = true
		}
		out = append(out, map[string]any{
			"client_order_index": id,
			"side":               state.Side,
			"price":              state.Price,
		})
	}
	return firstPrice, hasPrice, out
}

func (b *Bot) reconcileOrderState(ctx context.Context, ord Order) {
	b.mu.Lock()
	shuttingDown := b.shuttingDown
	b.mu.Unlock()
	if shuttingDown {
		return
	}
	switch ord.Status {
	case "filled":
		b.reconcileFilledOrder(ctx, ord)
	case "expired", "canceled", "canceled-expired":
		b.reconcileExpiredOrder(ctx, ord)
	}
}

func (b *Bot) reconcileExpiredOrder(ctx context.Context, ord Order) {
	snap, ok := b.claimTrackedOrder(ord.ClientOrderIndex)
	if !ok {
		return
	}
	if snap.LevelIndex < 0 {
		b.releaseTrackedOrder(ord.ClientOrderIndex, false)
		b.logger.Error("tracked order level is outside grid", "price", snap.BuyPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		return
	}
	if snap.LevelIndex >= len(b.grid) && snap.Side == "sell" {
		lvl := GridLevel{Index: snap.LevelIndex, BuyPrice: snap.BuyPrice, TPPrice: snap.TPPrice}
		b.logger.Info("Reduced-Only limit TP SELL expired/canceled → Replacing recovered overflow unit", "price", snap.TPPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		placedLvl, resp, clientID, err := b.placeReduceOnlyTPSell(ctx, lvl, snap.SizeAtomic, snap.TPPrice)
		if err != nil {
			b.releaseTrackedOrder(ord.ClientOrderIndex, false)
			b.logger.Error("replace expired tp failed", "price", snap.TPPrice, "error", err)
			return
		}
		b.logger.Info("Re-placing Reduced-Only limit TP SELL", "price", placedLvl.TPPrice, "size", b.cfg.OrderSize, "client_order_index", clientID, "tx_hash", resp.TxHash)
		b.trackOrder(placedLvl, "sell", placedLvl.TPPrice, snap.SizeAtomic, clientID)
		b.releaseTrackedOrder(ord.ClientOrderIndex, true)
		return
	}
	if snap.LevelIndex >= len(b.grid) {
		b.releaseTrackedOrder(ord.ClientOrderIndex, false)
		b.logger.Error("tracked order level is outside grid", "price", snap.BuyPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		return
	}

	lvl := GridLevel{Index: snap.LevelIndex, BuyPrice: snap.BuyPrice, TPPrice: snap.TPPrice}
	if snap.Side == "buy" {
		b.logger.Info("limit BUY expired/canceled → Replacing same grid level", "price", snap.BuyPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
		resp, clientID, err := b.ex.PlaceLimitOrder(ctx, "buy", lvl, snap.SizeAtomic, b.market.PriceDecimals, false)
		if err != nil {
			b.releaseTrackedOrder(ord.ClientOrderIndex, false)
			b.logger.Error("replace expired buy failed", "price", snap.BuyPrice, "error", err)
			return
		}
		b.logger.Info("Re-placing limit BUY order", "price", snap.BuyPrice, "size", b.cfg.OrderSize, "client_order_index", clientID, "tx_hash", resp.TxHash)
		b.trackOrder(lvl, "buy", snap.BuyPrice, snap.SizeAtomic, clientID)
		b.releaseTrackedOrder(ord.ClientOrderIndex, true)
		return
	}
	b.logger.Info("Reduced-Only limit TP SELL expired/canceled → Replacing same grid level", "price", snap.TPPrice, "client_order_index", ord.ClientOrderIndex, "level", snap.LevelIndex)
	placedLvl, resp, clientID, err := b.placeReduceOnlyTPSell(ctx, lvl, snap.SizeAtomic, snap.TPPrice)
	if err != nil {
		b.releaseTrackedOrder(ord.ClientOrderIndex, false)
		b.logger.Error("replace expired tp failed", "price", snap.TPPrice, "error", err)
		return
	}
	b.logger.Info("Re-placing Reduced-Only limit TP SELL", "price", placedLvl.TPPrice, "size", b.cfg.OrderSize, "client_order_index", clientID, "tx_hash", resp.TxHash)
	b.trackOrder(placedLvl, "sell", placedLvl.TPPrice, snap.SizeAtomic, clientID)
	b.releaseTrackedOrder(ord.ClientOrderIndex, true)
}

func (b *Bot) reconcilePositionCoverage(ctx context.Context) error {
	if len(b.grid) == 0 {
		return nil
	}
	b.mu.Lock()
	nextPlaceAt := b.nextCoveragePlaceAt
	b.mu.Unlock()
	if !nextPlaceAt.IsZero() && time.Now().Before(nextPlaceAt) {
		return nil
	}
	acct, err := b.ex.Account(ctx)
	if err != nil {
		b.setPollCooldown(err)
		return err
	}
	posUnits, err := positionUnitsForMarket(acct.Positions, b.market.MarketID, b.cfg.OrderSize)
	if err != nil {
		return err
	}
	activeOrders, err := b.ex.ActiveOrders(ctx)
	if err != nil {
		b.setPollCooldown(err)
		return err
	}
	activeBuys, activeTPs := classifyActiveGridOrders(activeOrders, b.market.MarketID)
	sizeAtomic := sizeToAtomic(b.cfg.OrderSize, b.market.SizeDecimals)
	if sizeAtomic <= 0 {
		return fmt.Errorf("invalid size atomic amount")
	}
	b.trackActiveOrders(activeBuys, activeTPs, sizeAtomic)

	tpUnits := activeTPUnits(activeTPs, sizeAtomic)
	activeTPIDs := make(map[int64]struct{})
	for _, ords := range activeTPs {
		for _, ord := range ords {
			activeTPIDs[ord.ClientOrderIndex] = struct{}{}
		}
	}
	b.pruneStaleTrackedTPs(activeTPIDs, inFlightTPWindow)
	inFlightUnits := b.inFlightTPUnits(activeTPIDs, inFlightTPWindow)
	covered := tpUnits + inFlightUnits
	if covered >= posUnits {
		return nil
	}
	missing := posUnits - covered
	market, err := b.ex.Market(ctx)
	if err != nil {
		b.setPollCooldown(err)
		return err
	}
	markPrice, err := parseFloat(market.MarkPrice)
	if err != nil || markPrice <= 0 {
		b.setPollCooldown(fmt.Errorf("coverage recovery requires valid mark price: %v", err))
		return fmt.Errorf("coverage recovery requires valid mark price: %w", err)
	}
	prevAtomic := int64(0)
	placed := 0
	for k := 0; k < missing; k++ {
		var lvl GridLevel
		lvl, prevAtomic = b.startupRecoveryLevel(k, markPrice, prevAtomic)
		b.logger.Warn("missing TP coverage detected → Placing Reduced-Only limit TP SELL", "price", lvl.TPPrice, "size", b.cfg.OrderSize, "level", lvl.Index, "position_units", posUnits, "active_tp_units", tpUnits, "in_flight_tp_units", inFlightUnits)
		placedLvl, resp, clientID, err := b.placeReduceOnlyTPSell(ctx, lvl, sizeAtomic, markPrice)
		if err != nil {
			b.logger.Error("missing TP recovery placement failed", "price", lvl.TPPrice, "level", lvl.Index, "error", err)
			b.setPollCooldown(err)
			return err
		}
		b.logger.Info("Placing Reduced-Only limit TP SELL", "price", placedLvl.TPPrice, "size", b.cfg.OrderSize, "client_order_index", clientID, "tx_hash", resp.TxHash, "level", placedLvl.Index)
		b.trackOrder(placedLvl, "sell", placedLvl.TPPrice, sizeAtomic, clientID)
		placed++
	}
	if placed > 0 {
		b.mu.Lock()
		b.nextCoveragePlaceAt = time.Now().Add(coveragePlaceCooldown)
		b.mu.Unlock()
	}
	return nil
}

func (b *Bot) inFlightTPUnits(activeTPIDs map[int64]struct{}, window time.Duration) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	units := 0
	now := time.Now()
	for id, state := range b.tracked {
		if state.Side != "sell" {
			continue
		}
		if _, ok := activeTPIDs[id]; ok {
			continue
		}
		if now.Sub(state.PlacedAt) > window {
			continue
		}
		units++
	}
	return units
}

func (b *Bot) pruneStaleTrackedTPs(activeTPIDs map[int64]struct{}, window time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for id, state := range b.tracked {
		if state.Side != "sell" {
			continue
		}
		if _, ok := activeTPIDs[id]; ok {
			continue
		}
		if now.Sub(state.PlacedAt) <= window {
			continue
		}
		delete(b.tracked, id)
	}
}

func (b *Bot) placeReduceOnlyTPSell(ctx context.Context, lvl GridLevel, sizeAtomic int64, fallbackPrice float64) (GridLevel, SendTxResponse, int64, error) {
	resp, clientID, err := b.ex.PlaceLimitOrder(ctx, "sell", lvl, sizeAtomic, b.market.PriceDecimals, true)
	if err == nil {
		return lvl, resp, clientID, nil
	}
	if !b.IsPriceBandError(err) || fallbackPrice <= 0 {
		return lvl, SendTxResponse{}, 0, err
	}
	retryLvl := lvl
	fallbackAtomic := priceToAtomic(fallbackPrice, b.market.PriceDecimals)
	lvlAtomic := priceToAtomic(lvl.TPPrice, b.market.PriceDecimals)
	baseAtomic := max64(fallbackAtomic, lvlAtomic)
	retryAtomic := baseAtomic + int64(max(1, lvl.Index))
	retryLvl.TPPrice = float64(retryAtomic) / math.Pow10(int(b.market.PriceDecimals))
	b.logger.Warn("Reduced-Only limit TP SELL rejected by price band; retrying with fallback price", "price", lvl.TPPrice, "fallback_price", fallbackPrice, "retry_price", retryLvl.TPPrice, "level", lvl.Index, "error", err)
	resp, clientID, err = b.ex.PlaceLimitOrder(ctx, "sell", retryLvl, sizeAtomic, b.market.PriceDecimals, true)
	if err != nil {
		return lvl, SendTxResponse{}, 0, err
	}
	return retryLvl, resp, clientID, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (b *Bot) trackActiveOrders(activeBuys map[int]Order, activeTPs map[int][]Order, sizeAtomic int64) {
	profitStep := 1 + b.cfg.ProfitPct/100
	if profitStep <= 0 {
		profitStep = 1
	}
	for level, ord := range activeBuys {
		if level < 0 || level >= len(b.grid) {
			continue
		}
		buy := parseOrderPrice(ord, b.grid[level].BuyPrice)
		lvl := GridLevel{Index: level, BuyPrice: buy, TPPrice: roundPrice(buy*profitStep, b.market.PriceDecimals)}
		b.trackOrderIfMissing(lvl, "buy", buy, sizeAtomic, ord.ClientOrderIndex)
	}
	for level, ords := range activeTPs {
		if level < 0 {
			continue
		}
		for _, ord := range ords {
			tp := parseOrderPrice(ord, b.gridLevelForIndex(level).TPPrice)
			lvl := GridLevel{Index: level, BuyPrice: roundPrice(tp/profitStep, b.market.PriceDecimals), TPPrice: tp}
			b.trackOrderIfMissing(lvl, "sell", tp, sizeAtomic, ord.ClientOrderIndex)
		}
	}
}

func parseOrderPrice(ord Order, fallback float64) float64 {
	if p, err := parseFloat(ord.Price); err == nil && p > 0 {
		return p
	}
	return fallback
}

func (b *Bot) gridLevelForIndex(level int) GridLevel {
	if level < len(b.grid) {
		return b.grid[level]
	}
	if len(b.grid) == 0 {
		return GridLevel{Index: level}
	}
	step := 1 - (b.cfg.GridSpacing / 100)
	if step <= 0 {
		return GridLevel{Index: level, BuyPrice: b.grid[len(b.grid)-1].BuyPrice, TPPrice: b.grid[len(b.grid)-1].BuyPrice * (1 + b.cfg.ProfitPct/100)}
	}
	offset := level - len(b.grid) + 1
	buy := b.grid[len(b.grid)-1].BuyPrice * math.Pow(step, float64(offset))
	return GridLevel{Index: level, BuyPrice: buy, TPPrice: buy * (1 + b.cfg.ProfitPct/100)}
}

func (b *Bot) trackOrderIfMissing(lvl GridLevel, side string, price float64, sizeAtomic int64, clientID int64) {
	b.mu.Lock()
	if _, ok := b.tracked[clientID]; ok {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.trackOrder(lvl, side, price, sizeAtomic, clientID)
}

func chunkClientOrderIDs(ids []int64, size int) [][]int64 {
	if size <= 0 {
		size = len(ids)
	}
	if size <= 0 {
		return nil
	}
	out := make([][]int64, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		chunk := make([]int64, end-start)
		copy(chunk, ids[start:end])
		out = append(out, chunk)
	}
	return out
}

func (b *Bot) checkFreeMargin(ctx context.Context) error {
	if b.cfg.DryRun {
		return nil
	}
	pct, err := b.ex.FreeMarginPct(ctx)
	if err != nil {
		return err
	}
	b.logger.Info("free margin check", "free_margin_pct", pct)
	if pct < 25 && !b.freeMarginWarned {
		b.freeMarginWarned = true
		msg := fmt.Sprintf("WARNING: Free margin < 25%% on %s Perpetual - Current: %.2f%%", b.cfg.ExchangeName, pct)
		b.logger.Warn(msg, "free_margin_pct", pct)
		_ = b.telegram.Send(ctx, msg)
	}
	if pct >= 25 {
		b.freeMarginWarned = false
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
