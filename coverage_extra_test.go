package gridcore

import (
	"context"
	"errors"
	"testing"
)

func TestIsAccidentalPriceError(t *testing.T) {
	if isAccidentalPriceError(nil) {
		t.Fatal("nil is not an accidental price error")
	}
	if !isAccidentalPriceError(errors.New("order price flagged as an ACCIDENTAL PRICE")) {
		t.Fatal("message match expected")
	}
	if !isAccidentalPriceError(errors.New(`sendTx failed: {"code":21733,"message":"x"}`)) {
		t.Fatal("code match expected")
	}
	if isAccidentalPriceError(errors.New("insufficient margin")) {
		t.Fatal("unrelated error must not match")
	}
}

func TestIsActiveOrderStatus(t *testing.T) {
	active := []string{"", "open", "ACTIVE", " pending ", "accepted", "partially-filled", "partial_filled", "partially_filled"}
	for _, s := range active {
		if !isActiveOrderStatus(s) {
			t.Fatalf("expected %q active", s)
		}
	}
	for _, s := range []string{"filled", "canceled", "expired", "canceled-expired", "rejected"} {
		if isActiveOrderStatus(s) {
			t.Fatalf("expected %q inactive", s)
		}
	}
}

func TestOrderMatchesMarket(t *testing.T) {
	if !orderMatchesMarket(Order{MarketIndex: 3}, 3) {
		t.Fatal("market index match")
	}
	if !orderMatchesMarket(Order{MarketID: 3}, 3) {
		t.Fatal("market id match")
	}
	if !orderMatchesMarket(Order{}, 0) {
		t.Fatal("zero market should match zero")
	}
	if orderMatchesMarket(Order{MarketIndex: 2}, 3) {
		t.Fatal("mismatch must be false")
	}
	if orderMatchesMarket(Order{}, 3) {
		t.Fatal("zero market must not match nonzero")
	}
}

func TestActiveTPUnits(t *testing.T) {
	orders := map[int][]Order{
		0: {{RemainingBaseAmount: "10"}},
		1: {{RemainingBaseAmount: "10"}, {RemainingBaseAmount: ""}},
	}
	if got := activeTPUnits(orders, 0); got != 3 {
		t.Fatalf("count mode = %d, want 3", got)
	}
	if got := activeTPUnits(orders, 10); got != 3 {
		t.Fatalf("unit mode = %d, want 3", got)
	}
	partial := map[int][]Order{0: {{RemainingBaseAmount: "5"}}}
	if got := activeTPUnits(partial, 10); got != 0 {
		t.Fatalf("partial unit = %d, want 0", got)
	}
}

func TestReconcileFilledOrderOverflowTPReleased(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	id := EncodeTPClientOrderID(5)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "sell", LevelIndex: 5, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: id, Status: "filled", IsAsk: true})
	if len(sx.placed) != 0 {
		t.Fatalf("overflow TP fill must not place orders, got %#v", sx.placed)
	}
	b.mu.Lock()
	_, exists := b.tracked[id]
	b.mu.Unlock()
	if exists {
		t.Fatal("settled overflow TP should be removed")
	}
}

func TestReconcileFilledOrderNegativeLevelReleased(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	b.tracked[7] = &TrackedOrder{ClientOrderID: 7, Side: "buy", LevelIndex: -1, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: 7, Status: "filled"})
	if len(sx.placed) != 0 {
		t.Fatalf("negative level must not place orders, got %#v", sx.placed)
	}
}

func TestReconcileFilledOrderOverflowBuyReleased(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	id := EncodeBuyClientOrderID(5)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "buy", LevelIndex: 5, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: id, Status: "filled", IsAsk: false})
	if len(sx.placed) != 0 {
		t.Fatalf("overflow buy fill must not place orders, got %#v", sx.placed)
	}
}

func TestReconcileFilledOrderRestoreBuyError(t *testing.T) {
	b := testBot(&genericPlaceErrorVenue{err: errors.New("boom")})
	id := EncodeTPClientOrderID(0)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "sell", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: id, Status: "filled", IsAsk: true})
	b.mu.Lock()
	state, exists := b.tracked[id]
	b.mu.Unlock()
	if !exists || state.Processing {
		t.Fatal("failed restore should keep the order tracked and unclaimed")
	}
}

func TestReconcileFilledOrderTPPlacementError(t *testing.T) {
	b := testBot(&genericPlaceErrorVenue{err: errors.New("boom")})
	id := EncodeBuyClientOrderID(0)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: id, Status: "filled", IsAsk: false})
	b.mu.Lock()
	state, exists := b.tracked[id]
	b.mu.Unlock()
	if !exists || state.Processing {
		t.Fatal("failed TP placement should keep the buy tracked and unclaimed")
	}
}

func TestReconcileFilledOrderAlreadyClaimedIsIgnored(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	id := EncodeBuyClientOrderID(0)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10, Processing: true}
	b.reconcileFilledOrder(context.Background(), Order{ClientOrderIndex: id, Status: "filled"})
	if len(sx.placed) != 0 {
		t.Fatalf("claimed order should be skipped, got %#v", sx.placed)
	}
}

func TestReconcileExpiredOrderOverflowTPReplaced(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	id := EncodeTPClientOrderID(5)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "sell", LevelIndex: 5, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: id, Status: "expired"})
	if len(sx.placed) != 1 || sx.placed[0] != "sell" {
		t.Fatalf("overflow TP should be re-placed, got %#v", sx.placed)
	}
}

func TestReconcileExpiredOrderOverflowBuyReleased(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	id := EncodeBuyClientOrderID(5)
	b.tracked[id] = &TrackedOrder{ClientOrderID: id, Side: "buy", LevelIndex: 5, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: id, Status: "expired"})
	if len(sx.placed) != 0 {
		t.Fatalf("overflow buy expiry must not place orders, got %#v", sx.placed)
	}
}

func TestReconcileExpiredOrderNegativeLevelReleased(t *testing.T) {
	sx := &stubExchange{}
	b := testBot(sx)
	b.tracked[7] = &TrackedOrder{ClientOrderID: 7, Side: "sell", LevelIndex: -1, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	b.reconcileExpiredOrder(context.Background(), Order{ClientOrderIndex: 7, Status: "canceled"})
	if len(sx.placed) != 0 {
		t.Fatalf("negative level must not be re-placed, got %#v", sx.placed)
	}
}

func TestReconcileExpiredOrderPlacementErrors(t *testing.T) {
	ctx := context.Background()
	buyBot := testBot(&genericPlaceErrorVenue{err: errors.New("boom")})
	buyID := EncodeBuyClientOrderID(0)
	buyBot.tracked[buyID] = &TrackedOrder{ClientOrderID: buyID, Side: "buy", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	buyBot.reconcileExpiredOrder(ctx, Order{ClientOrderIndex: buyID, Status: "expired"})

	sellBot := testBot(&genericPlaceErrorVenue{err: errors.New("boom")})
	sellID := EncodeTPClientOrderID(0)
	sellBot.tracked[sellID] = &TrackedOrder{ClientOrderID: sellID, Side: "sell", LevelIndex: 0, BuyPrice: 100, TPPrice: 101, SizeAtomic: 10}
	sellBot.reconcileExpiredOrder(ctx, Order{ClientOrderIndex: sellID, Status: "expired"})

	for name, bot := range map[string]*Bot{"buy": buyBot, "sell": sellBot} {
		bot.mu.Lock()
		n := len(bot.tracked)
		processing := false
		for _, s := range bot.tracked {
			if s.Processing {
				processing = true
			}
		}
		bot.mu.Unlock()
		if n != 1 || processing {
			t.Fatalf("%s: failed replacement should keep one unclaimed order", name)
		}
	}
}
