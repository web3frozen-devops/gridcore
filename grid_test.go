package gridcore

import "testing"

func TestBuildGrid(t *testing.T) {
	grid := buildGrid(100000, 0.2, 0.2, 5)
	if len(grid) != 5 {
		t.Fatalf("expected 5 levels, got %d", len(grid))
	}
	if got := grid[0].BuyPrice; got < 99799.9 || got > 99800.1 {
		t.Fatalf("unexpected first buy price: %v", got)
	}
	if got := grid[0].TPPrice; got < 99999.5 || got > 99999.7 {
		t.Fatalf("unexpected first TP price: %v", got)
	}
}

func TestClientOrderIDEncoding(t *testing.T) {
	buyID := encodeBuyClientOrderID(3)
	anotherBuyID := encodeBuyClientOrderID(3)
	if buyID == anotherBuyID {
		t.Fatalf("expected same-level buy client order IDs to be unique, got %d", buyID)
	}
	if level, side, ok := decodeLevelFromClientOrderID(buyID); !ok || side != "buy" || level != 3 {
		t.Fatalf("bad buy encoding")
	}
	if level, side, ok := decodeLevelFromClientOrderID(encodeTPClientOrderID(4)); !ok || side != "tp" || level != 4 {
		t.Fatalf("bad tp encoding")
	}
	if level, side, ok := decodeLevelFromClientOrderID(1_000_000_003); !ok || side != "buy" || level != 3 {
		t.Fatalf("bad legacy buy decoding")
	}
	if level, side, ok := decodeLevelFromClientOrderID(2_000_000_004); !ok || side != "tp" || level != 4 {
		t.Fatalf("bad legacy tp decoding")
	}
}
