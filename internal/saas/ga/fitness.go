package ga

// FitnessConfig parameterises the score aggregation. Defaults match
// docs/20-進化計算引擎.md §3.1.
type FitnessConfig struct {
	DDPenaltyCoef     float64
	FatalThresholds   map[string]float64 // window label → MaxDD threshold
	FatalPenalties    map[string]float64 // window label → flat slice score on fatal
}

// DefaultFitnessConfig matches the spec table in §3.1:
//   6m  threshold 0.50 → penalty -200
//   2y  threshold 0.75 → penalty -100
//   5y  threshold 0.88 → penalty  -10
//   10y threshold 0.95 → penalty  -10
//
// The recency-weighted fatal penalties are intentionally NOT multiplied
// by the window weight inside this function — the aggregator multiplies
// once when summing, and double-weighting here would suppress the gating
// effect (round-2 review fix).
func DefaultFitnessConfig() FitnessConfig {
	return FitnessConfig{
		DDPenaltyCoef: 1.5,
		FatalThresholds: map[string]float64{
			"6m":  0.50,
			"2y":  0.75,
			"5y":  0.88,
			"10y": 0.95,
		},
		FatalPenalties: map[string]float64{
			"6m":  -200,
			"2y":  -100,
			"5y":  -10,
			"10y": -10,
		},
	}
}

// SliceScore returns the per-window fitness for one (strategy, baseline) pair.
//
// Formula (spec §3.1):
//   if MaxDD_strategy >= FatalThreshold[label]:
//       return FatalPenalty[label]   // flat — NOT multiplied by weight here
//   else:
//       return Alpha - DDPenaltyCoef × max(0, MaxDD_strategy - MaxDD_baseline)
//   where Alpha = ROI_strategy - ROI_baseline
func SliceScore(label string, stratROI, stratDD, baselineROI, baselineDD float64,
	cfg FitnessConfig) float64 {

	if thr, ok := cfg.FatalThresholds[label]; ok && stratDD >= thr {
		if pen, ok := cfg.FatalPenalties[label]; ok {
			return pen
		}
		return -10 // sane fallback if penalty map is incomplete
	}

	alpha := stratROI - baselineROI
	excessDD := stratDD - baselineDD
	if excessDD < 0 {
		excessDD = 0
	}
	return alpha - cfg.DDPenaltyCoef*excessDD
}

// ScoreTotal aggregates per-window slice scores by their CrucibleWindow
// weights. The aggregator multiplies the slice score by Window.Weight
// EXACTLY ONCE — fatal penalties already arrive un-weighted from SliceScore
// (this is the round-2 review fix).
func ScoreTotal(windows []CrucibleWindow, sliceScores []float64) float64 {
	var sum float64
	n := len(windows)
	if n != len(sliceScores) {
		// Caller bug; return a low score to make this individual lose.
		return -1e9
	}
	for i := 0; i < n; i++ {
		sum += sliceScores[i] * windows[i].Weight
	}
	return sum
}
