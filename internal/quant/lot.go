package quant

import (
	"sort"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// LotType labels (kept here for callers that don't want to drag in the
// strategy package just for these constants).
const (
	LotTypeDead       = "DEAD"
	LotTypeFloat      = "FLOAT"
	LotTypeColdSealed = "COLD_SEALED"
)

// LotFilter is a predicate over a lot. Used to compose selection logic
// (by type, age, cold-sealed flag, etc.) without sprawling boolean
// expressions in strategy code.
type LotFilter func(strategy.LotEntry) bool

// FilterLots returns a fresh slice containing only lots that match the
// provided filter. The order of the input is preserved.
func FilterLots(lots []strategy.LotEntry, f LotFilter) []strategy.LotEntry {
	out := make([]strategy.LotEntry, 0, len(lots))
	for _, l := range lots {
		if f(l) {
			out = append(out, l)
		}
	}
	return out
}

// IsType returns a LotFilter that matches the given Type value (e.g. "DEAD").
func IsType(typ string) LotFilter {
	return func(l strategy.LotEntry) bool { return l.Type == typ }
}

// NotColdSealed returns a LotFilter excluding cold-sealed lots — these
// are never released no matter how old they are.
func NotColdSealed() LotFilter {
	return func(l strategy.LotEntry) bool { return !l.IsColdSealed }
}

// AgedAtLeast returns a LotFilter that matches lots with BuyTimeMs at least
// minAgeMs older than nowMs. Useful for soft-release aging gates.
func AgedAtLeast(nowMs, minAgeMs int64) LotFilter {
	return func(l strategy.LotEntry) bool {
		return nowMs-l.BuyTimeMs >= minAgeMs
	}
}

// And returns a LotFilter that matches when ALL provided filters match.
func And(filters ...LotFilter) LotFilter {
	return func(l strategy.LotEntry) bool {
		for _, f := range filters {
			if !f(l) {
				return false
			}
		}
		return true
	}
}

// SortByBuyTimeMsAsc returns a fresh slice sorted ascending by BuyTimeMs
// (oldest first — FIFO order for releasing).
func SortByBuyTimeMsAsc(lots []strategy.LotEntry) []strategy.LotEntry {
	out := make([]strategy.LotEntry, len(lots))
	copy(out, lots)
	sort.SliceStable(out, func(i, j int) bool { return out[i].BuyTimeMs < out[j].BuyTimeMs })
	return out
}

// SumQty returns the total Qty across the given lots. Returns 0 for empty.
func SumQty(lots []strategy.LotEntry) float64 {
	var sum float64
	for _, l := range lots {
		sum += l.Qty
	}
	return sum
}

// AccumulateSlices walks lots in the supplied order and produces ReleaseSlice
// entries up to (but not exceeding) targetQty. Supports partial slicing of
// the last lot to hit the exact targetQty.
//
// Returns:
//   slices  - the per-lot ReleaseSlice entries (FIFO consumed)
//   filled  - sum(slices.Qty); ≤ targetQty
//
// If lots can't satisfy targetQty, slices contains everything and filled
// is the available total. The caller decides how to react (e.g. cap the
// SELL order to remainingFloatQty + filled).
//
// targetQty < 0 is treated as 0.
func AccumulateSlices(lots []strategy.LotEntry, targetQty float64) (slices []strategy.ReleaseSlice, filled float64) {
	if targetQty <= 0 {
		return nil, 0
	}
	for _, l := range lots {
		if filled >= targetQty {
			break
		}
		remaining := targetQty - filled
		take := l.Qty
		if take > remaining {
			take = remaining
		}
		if take <= 0 {
			continue
		}
		slices = append(slices, strategy.ReleaseSlice{LotID: l.ID, Qty: take})
		filled += take
	}
	return slices, filled
}
