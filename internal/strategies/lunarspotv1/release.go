package lunarspotv1

import (
	"github.com/Chuanyin1202/eighti-quant/internal/quant"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

const (
	msPerDay  = int64(24 * 60 * 60 * 1000)
	daysMonth = int64(30) // approximate; matches "DeadAgingMonths × 30 days" in spec
)

// maybeSoftRelease produces a DEAD → FLOAT release when:
//   1. CurrentMicroWeight < TargetMicroWeightFloor (micro engine is "low on
//      ammo"), and
//   2. there are eligible DEAD lots — non-cold-sealed, aged ≥ DeadAgingMonths
//
// Returns nil if either condition fails.
//
// RelatedIntentIndex is set to -1 (soft releases don't pair with a SELL).
func maybeSoftRelease(
	lots []strategy.LotEntry,
	currentMicroWeight, totalEquity, livePrice float64,
	chromo Chromosome,
	latestBarTimeMs int64,
) *strategy.ReleaseIntent {

	if currentMicroWeight >= chromo.TargetMicroWeightFloor {
		return nil
	}

	// Filter eligible DEAD lots: non-sealed, aged enough.
	minAgeMs := int64(chromo.DeadAgingMonths) * daysMonth * msPerDay
	eligible := quant.FilterLots(lots, quant.And(
		quant.IsType(quant.LotTypeDead),
		quant.NotColdSealed(),
		quant.AgedAtLeast(latestBarTimeMs, minAgeMs),
	))
	if len(eligible) == 0 {
		return nil
	}

	// Compute target qty: bring micro weight up to the floor.
	weightGap := chromo.TargetMicroWeightFloor - currentMicroWeight
	if weightGap <= 0 || livePrice <= 0 {
		return nil
	}
	targetQty := (weightGap * totalEquity) / livePrice

	// FIFO consume from oldest.
	sorted := quant.SortByBuyTimeMsAsc(eligible)
	slices, filled := quant.AccumulateSlices(sorted, targetQty)
	if filled <= 0 || len(slices) == 0 {
		return nil
	}

	return &strategy.ReleaseIntent{
		Kind:               "soft_release",
		Slices:             slices,
		TotalQtyAsset:      filled,
		RelatedIntentIndex: -1,
	}
}

// maybeHardRelease produces a DEAD → FLOAT release paired with a micro SELL.
//
// Caller decides relatedIntentIndex (= the SELL's index in the final
// StrategyOutput.Intents). The release Slices are FIFO across all eligible
// DEAD non-sealed lots — aging gate is intentionally NOT applied here:
// hard releases are rescue ops triggered by genuine inventory shortage,
// not opportunistic rebalancing.
//
// Returns nil if no inventory at all is borrowable.
func maybeHardRelease(
	lots []strategy.LotEntry,
	deficitQty float64,
	relatedIntentIndex int,
) *strategy.ReleaseIntent {

	if deficitQty <= 0 {
		return nil
	}

	eligible := quant.FilterLots(lots, quant.And(
		quant.IsType(quant.LotTypeDead),
		quant.NotColdSealed(),
	))
	if len(eligible) == 0 {
		return nil
	}

	sorted := quant.SortByBuyTimeMsAsc(eligible)
	slices, filled := quant.AccumulateSlices(sorted, deficitQty)
	if filled <= 0 || len(slices) == 0 {
		return nil
	}

	return &strategy.ReleaseIntent{
		Kind:               "hard_release",
		Slices:             slices,
		TotalQtyAsset:      filled,
		RelatedIntentIndex: relatedIntentIndex,
	}
}
