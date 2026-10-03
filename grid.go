package gridcore

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	legacyBuyClientOrderBase int64 = 1_000_000_000
	legacyTPClientOrderBase  int64 = 2_000_000_000

	buyClientOrderBase    int64 = 100_000_000_000_000
	tpClientOrderBase     int64 = 200_000_000_000_000
	clientOrderLevelRange int64 = 1_000_000_000
	maxClientOrderIndex   int64 = (1 << 48) - 1
)

var clientOrderSequence atomic.Uint64

// randInt is crypto/rand.Int. It is a variable so tests can exercise the
// fallback path taken if the system entropy source ever fails.
var randInt = rand.Int

func resolveSymbol(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	s = strings.TrimSuffix(s, "-PERP")
	s = strings.TrimSuffix(s, "/USDT")
	s = strings.TrimSuffix(s, "-USDT")
	s = strings.TrimSuffix(s, "_PERP")
	return s
}

func buildGrid(markPrice, spacingPct, profitPct float64, levels int) []GridLevel {
	grid := make([]GridLevel, 0, levels)
	for i := 0; i < levels; i++ {
		buy := markPrice * math.Pow(1-spacingPct/100, float64(i+1))
		tp := buy * (1 + profitPct/100)
		grid = append(grid, GridLevel{Index: i, BuyPrice: buy, TPPrice: tp})
	}
	return grid
}

func sizeToAtomic(size float64, decimals uint8) int64 {
	return int64(math.Round(size * math.Pow10(int(decimals))))
}

func priceToAtomic(price float64, decimals uint8) int64 {
	return int64(math.Round(price * math.Pow10(int(decimals))))
}

func atomicToPrice(amount int64, decimals uint8) float64 {
	return float64(amount) / math.Pow10(int(decimals))
}

func roundPrice(price float64, decimals uint8) float64 {
	return atomicToPrice(priceToAtomic(price, decimals), decimals)
}

func atomicToSize(amount int64, decimals uint8) float64 {
	return float64(amount) / math.Pow10(int(decimals))
}

func encodeBuyClientOrderID(level int) int64 {
	return encodeClientOrderID(buyClientOrderBase, level)
}

func encodeTPClientOrderID(level int) int64 {
	return encodeClientOrderID(tpClientOrderBase, level)
}

func encodeClientOrderID(base int64, level int) int64 {
	if level < 0 {
		level = 0
	}
	// Enforce the shared encodable bound so both the buy and TP ranges decode
	// back to exactly the requested level. Levels above the bound fall back to
	// level 0 instead of silently overlapping or overflowing the index space.
	if level > maxClientOrderLevel {
		level = 0
	}
	// maxClientOrderLevel is derived so that, with a suffix strictly below
	// clientOrderLevelRange, the encoded id can never exceed
	// maxClientOrderIndex nor cross into the other side's range.
	return base + int64(level)*clientOrderLevelRange + nextClientOrderSuffix()
}

func nextClientOrderSuffix() int64 {
	seq := int64(clientOrderSequence.Add(1) % 1_000)
	n, err := randInt(rand.Reader, big.NewInt(clientOrderLevelRange/1_000))
	if err == nil {
		return n.Int64()*1_000 + seq
	}
	return (time.Now().UnixNano()%(clientOrderLevelRange/1_000))*1_000 + seq
}

func decodeLevelFromClientOrderID(clientOrderID int64) (int, string, bool) {
	switch {
	case clientOrderID >= buyClientOrderBase && clientOrderID < tpClientOrderBase:
		return int((clientOrderID - buyClientOrderBase) / clientOrderLevelRange), "buy", true
	case clientOrderID >= tpClientOrderBase && clientOrderID <= maxClientOrderIndex:
		return int((clientOrderID - tpClientOrderBase) / clientOrderLevelRange), "tp", true
	case clientOrderID >= legacyBuyClientOrderBase && clientOrderID < legacyBuyClientOrderBase+1_000_000:
		return int(clientOrderID - legacyBuyClientOrderBase), "buy", true
	case clientOrderID >= legacyTPClientOrderBase && clientOrderID < legacyTPClientOrderBase+1_000_000:
		return int(clientOrderID - legacyTPClientOrderBase), "tp", true
	default:
		return 0, "", false
	}
}

func isActiveOrderStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "open", "active", "pending", "accepted", "partially-filled", "partial-filled", "partially_filled", "partial_filled":
		return true
	default:
		return false
	}
}

func positionUnitsForMarket(positions []AccountPosition, marketID int16, orderSize float64) (int, error) {
	for _, p := range positions {
		if p.MarketID != marketID {
			continue
		}
		if p.Sign < 0 {
			return 0, fmt.Errorf("short position detected; bot only supports long positions")
		}
		return positionUnits(p.Position, orderSize), nil
	}
	return 0, nil
}

func activeTPUnits(activeTPs map[int][]Order, sizeAtomic int64) int {
	if sizeAtomic <= 0 {
		units := 0
		for _, ords := range activeTPs {
			units += len(ords)
		}
		return units
	}
	total := int64(0)
	for _, ords := range activeTPs {
		for _, ord := range ords {
			remaining := orderRemainingAtomic(ord)
			if remaining <= 0 {
				remaining = sizeAtomic
			}
			total += remaining
		}
	}
	return int(total / sizeAtomic)
}

func orderRemainingAtomic(ord Order) int64 {
	s := strings.TrimSpace(ord.RemainingBaseAmount)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func classifyActiveGridOrders(orders []Order, marketID int16) (map[int]Order, map[int][]Order) {
	activeBuys := make(map[int]Order)
	activeTPs := make(map[int][]Order)
	for _, ord := range orders {
		if !orderMatchesMarket(ord, marketID) || !isActiveOrderStatus(ord.Status) {
			continue
		}
		level, side, ok := decodeLevelFromClientOrderID(ord.ClientOrderIndex)
		if !ok {
			continue
		}
		if side == "buy" && !ord.IsAsk {
			activeBuys[level] = ord
			continue
		}
		if side == "tp" && ord.IsAsk && ord.ReduceOnly {
			activeTPs[level] = append(activeTPs[level], ord)
		}
	}
	return activeBuys, activeTPs
}

func orderMatchesMarket(ord Order, marketID int16) bool {
	if ord.MarketIndex == marketID || ord.MarketID == marketID {
		return true
	}
	return ord.MarketIndex == 0 && ord.MarketID == 0 && marketID == 0
}

func positionUnits(position string, orderSize float64) int {
	f, err := parseFloat(position)
	if err != nil || orderSize <= 0 {
		return 0
	}
	units := int(math.Floor(math.Abs(f)/orderSize + 1e-9))
	return units
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func sortGridByLevel(grid []GridLevel) {
	sort.Slice(grid, func(i, j int) bool { return grid[i].Index < grid[j].Index })
}

func describePrice(price float64) string {
	return fmt.Sprintf("%.8f", price)
}

// maxClientOrderLevel is the largest grid level the client order index encoding
// can carry for either side. It is bounded by the tighter of the buy and TP id
// ranges, with one full level range reserved for the random per-call suffix so
// an encoded id can never spill past maxClientOrderIndex or cross into the other
// side's range. Levels above this fall back to a level-0 index (see
// encodeClientOrderID).
//
// Buy capacity: (tpClientOrderBase-buyClientOrderBase)/clientOrderLevelRange - 1 = 99_999
// TP  capacity: (maxClientOrderIndex-tpClientOrderBase-clientOrderLevelRange+1)/clientOrderLevelRange = 81_473
const maxClientOrderLevel = int((maxClientOrderIndex - tpClientOrderBase - clientOrderLevelRange) / clientOrderLevelRange)
