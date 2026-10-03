package gridcore

import (
	"errors"
	"testing"
)

func TestEncodeDecodeClientOrderIDRoundTrip(t *testing.T) {
	for _, level := range []int{0, 1, 5, 1000, maxClientOrderLevel} {
		buy := EncodeBuyClientOrderID(level)
		gotLevel, side, ok := DecodeClientOrderID(buy)
		if !ok || side != "buy" || gotLevel != level {
			t.Fatalf("buy level %d: got (%d,%q,%v)", level, gotLevel, side, ok)
		}
		tp := EncodeTPClientOrderID(level)
		gotLevel, side, ok = DecodeClientOrderID(tp)
		if !ok || side != "tp" || gotLevel != level {
			t.Fatalf("tp level %d: got (%d,%q,%v)", level, gotLevel, side, ok)
		}
	}
}

func TestEncodeClientOrderIDBeyondRangeFallsBack(t *testing.T) {
	// Levels above maxClientOrderLevel cannot be encoded in the level field; the
	// encoder falls back to a level-0 id rather than overflowing the index.
	lvl, side, ok := DecodeClientOrderID(EncodeBuyClientOrderID(maxClientOrderLevel + 1))
	if !ok || side != "buy" || lvl != 0 {
		t.Fatalf("expected overflow fallback to decode as level 0 buy, got (%d,%q,%v)", lvl, side, ok)
	}
}

func TestEncodeClientOrderIDUniquePerCall(t *testing.T) {
	a := EncodeBuyClientOrderID(3)
	b := EncodeBuyClientOrderID(3)
	if a == b {
		t.Fatalf("expected distinct ids for repeated same-level encodings, got %d", a)
	}
	if lvl, side, ok := DecodeClientOrderID(a); !ok || side != "buy" || lvl != 3 {
		t.Fatalf("first id decoded wrong: (%d,%q,%v)", lvl, side, ok)
	}
	if lvl, side, ok := DecodeClientOrderID(b); !ok || side != "buy" || lvl != 3 {
		t.Fatalf("second id decoded wrong: (%d,%q,%v)", lvl, side, ok)
	}
}

func TestEncodeClientOrderIDNegativeLevelClamped(t *testing.T) {
	if lvl, _, ok := DecodeClientOrderID(EncodeBuyClientOrderID(-5)); !ok || lvl != 0 {
		t.Fatalf("expected negative level to clamp to 0, got %d ok=%v", lvl, ok)
	}
}

func TestDecodeClientOrderIDUnknown(t *testing.T) {
	for _, id := range []int64{0, 1, 999, maxClientOrderIndex + 1} {
		if lvl, side, ok := DecodeClientOrderID(id); ok {
			t.Fatalf("id %d: expected failure, got (%d,%q)", id, lvl, side)
		}
	}
}

func TestConversionHelpers(t *testing.T) {
	if got := SizeToAtomic(0.01, 4); got != 100 {
		t.Fatalf("SizeToAtomic = %d, want 100", got)
	}
	if got := PriceToAtomic(99800.0, 2); got != 9980000 {
		t.Fatalf("PriceToAtomic = %d, want 9980000", got)
	}
	if got := AtomicToPrice(9980000, 2); got != 99800.0 {
		t.Fatalf("AtomicToPrice = %v, want 99800", got)
	}
	if got := AtomicToSize(100, 4); got != 0.01 {
		t.Fatalf("AtomicToSize = %v, want 0.01", got)
	}
	if got := RoundPrice(99804.999, 2); got != 99805.0 {
		t.Fatalf("RoundPrice = %v, want 99805", got)
	}
	if got := ResolveSymbol("btc-usdt-perp"); got != "BTC" {
		t.Fatalf("ResolveSymbol = %q, want BTC", got)
	}
}

type classifierVenue struct {
	stubExchange
	verdict bool
}

func (c *classifierVenue) IsPriceBandError(error) bool { return c.verdict }

func TestIsPriceBandErrorDefaultHeuristic(t *testing.T) {
	b := &Bot{ex: &stubExchange{}}
	if b.IsPriceBandError(nil) {
		t.Fatal("nil error must not be a price-band error")
	}
	if !b.IsPriceBandError(errors.New(`sendTx failed: {"code":21733,"message":"x"}`)) {
		t.Fatal("expected Lighter code 21733 to be a price-band error")
	}
	if !b.IsPriceBandError(errors.New("order price flagged as an accidental price")) {
		t.Fatal("expected accidental-price message to match")
	}
	if b.IsPriceBandError(errors.New("insufficient margin")) {
		t.Fatal("expected unrelated error to not match")
	}
}

func TestIsPriceBandErrorDelegatesToVenue(t *testing.T) {
	venue := &classifierVenue{verdict: true}
	b := &Bot{ex: venue}
	if !b.IsPriceBandError(errors.New("anything")) {
		t.Fatal("expected venue classifier to be consulted")
	}
	venue.verdict = false
	if b.IsPriceBandError(errors.New(`"code":21733`)) {
		t.Fatal("expected venue classifier to override the default heuristic")
	}
}
