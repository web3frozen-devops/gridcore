package gridcore

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestPreviewDoesNotPlaceOrders(t *testing.T) {
	sx := &stubExchange{
		marketResp:  MarketMeta{MarketID: 32, PriceDecimals: 2, SizeDecimals: 2, MarkPrice: "100"},
		accountResp: AccountResponse{Positions: []AccountPosition{{MarketID: 32, Sign: 1, Position: "0.05"}}},
	}
	b := &Bot{
		cfg:    Config{PreRun: true, OrderSize: 0.01, GridSpacing: 0.2, ProfitPct: 0.2, NumLevels: 5},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ex:     sx,
	}
	if err := b.preview(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sx.cancelAllHits != 0 || len(sx.placed) != 0 {
		t.Fatalf("expected no order flow in preview mode")
	}
}

func TestStartupRecoveryLevelUsesTopBuyTPAnchor(t *testing.T) {
	b := &Bot{
		cfg:    Config{GridSpacing: 0.2, ProfitPct: 0.2},
		market: MarketMeta{PriceDecimals: 2},
		grid:   buildGrid(100, 0.2, 0.2, 5),
	}
	first, prev := b.startupRecoveryLevel(0, 100, 0)
	second, _ := b.startupRecoveryLevel(1, 100, prev)
	if first.TPPrice <= 100 {
		t.Fatalf("expected first recovery TP above mark, got %.2f", first.TPPrice)
	}
	if second.TPPrice <= first.TPPrice {
		t.Fatalf("expected recovery TP ladder to increase, got %.2f then %.2f", first.TPPrice, second.TPPrice)
	}
	if first.TPPrice < 100.01 {
		t.Fatalf("unexpected first recovery TP price: %.2f", first.TPPrice)
	}
}
