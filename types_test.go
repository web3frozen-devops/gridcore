package gridcore

import (
	"encoding/json"
	"testing"
)

func TestAccountMarketUpdatePositionObject(t *testing.T) {
	raw := []byte(`{"channel":"account_market:32:4630","account":4630,"position":{"market_id":32,"symbol":"SNDK","sign":1,"position":"0.1"},"orders":[],"trades":[]}`)
	var update AccountMarketUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	if len(update.Position) != 1 {
		t.Fatalf("expected one position, got %d", len(update.Position))
	}
	if update.Position[0].MarketID != 32 {
		t.Fatalf("expected market 32, got %d", update.Position[0].MarketID)
	}
}

func TestAccountMarketUpdatePositionArray(t *testing.T) {
	raw := []byte(`{"position":[{"market_id":32,"symbol":"SNDK","sign":1,"position":"0.1"}]}`)
	var update AccountMarketUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	if len(update.Position) != 1 {
		t.Fatalf("expected one position, got %d", len(update.Position))
	}
}
