package gridcore

// Exported surface for venue adapters.
//
// A Venue implementation MUST encode the client order index of every order it
// submits with EncodeBuyClientOrderID / EncodeTPClientOrderID. The engine
// decodes those indexes (DecodeClientOrderID) to attribute fills, expiries and
// cancels to grid levels after a stateless restart. Adapters that invent their
// own client-id scheme will silently lose order tracking.

// EncodeBuyClientOrderID returns a client order index carrying the grid level
// for a BUY order. Successive calls for the same level return distinct ids.
func EncodeBuyClientOrderID(level int) int64 { return encodeBuyClientOrderID(level) }

// EncodeTPClientOrderID returns a client order index carrying the grid level
// for a reduce-only TP SELL order.
func EncodeTPClientOrderID(level int) int64 { return encodeTPClientOrderID(level) }

// DecodeClientOrderID recovers (level, side, ok) from a client order index
// produced by this package. side is "buy" or "tp".
func DecodeClientOrderID(clientOrderID int64) (level int, side string, ok bool) {
	return decodeLevelFromClientOrderID(clientOrderID)
}

// SizeToAtomic converts a base-asset size to integer atomic units.
func SizeToAtomic(size float64, decimals uint8) int64 { return sizeToAtomic(size, decimals) }

// PriceToAtomic converts a price to integer atomic units.
func PriceToAtomic(price float64, decimals uint8) int64 { return priceToAtomic(price, decimals) }

// AtomicToPrice converts integer atomic price units back to a float price.
func AtomicToPrice(amount int64, decimals uint8) float64 { return atomicToPrice(amount, decimals) }

// AtomicToSize converts integer atomic size units back to a float size.
func AtomicToSize(amount int64, decimals uint8) float64 { return atomicToSize(amount, decimals) }

// RoundPrice rounds a price to the venue's price decimals.
func RoundPrice(price float64, decimals uint8) float64 { return roundPrice(price, decimals) }

// ResolveSymbol normalizes a display symbol (e.g. "BTC-USDT-PERP", "BTC/USDT")
// to its bare base asset.
func ResolveSymbol(symbol string) string { return resolveSymbol(symbol) }

// PriceBandClassifier is an optional Venue extension. When a venue implements
// it, the engine uses IsPriceBandError to detect "order price outside the
// allowed band" rejections instead of the built-in Lighter heuristic, so it can
// retry with a fallback price or skip a level.
type PriceBandClassifier interface {
	IsPriceBandError(err error) bool
}

// IsPriceBandError reports whether err is a price-band rejection, delegating to
// the venue when it implements PriceBandClassifier.
func (b *Bot) IsPriceBandError(err error) bool {
	if err == nil {
		return false
	}
	if c, ok := b.ex.(PriceBandClassifier); ok {
		return c.IsPriceBandError(err)
	}
	return isAccidentalPriceError(err)
}
