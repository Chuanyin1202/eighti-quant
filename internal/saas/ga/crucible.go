package ga

import "fmt"

// CrucibleParams configures the four-window construction.
//
// WarmupBars is how many bars to prefix BEFORE each window's evaluation
// region (so warmup-dependent indicators are stable). 1200 is the spec
// default (~50 days of 4h data * 24 = ~1200 bars; covers EMA600 + sigma).
type CrucibleParams struct {
	IntervalHours int
	WarmupBars    int
}

// DefaultCrucibleParams returns the canonical configuration matching
// docs/20-進化計算引擎.md §1.1: 4h aggregation, 1200-bar warmup.
func DefaultCrucibleParams() CrucibleParams {
	return CrucibleParams{IntervalHours: 4, WarmupBars: 1200}
}

// BuildCrucibleWindows takes the full close-only price history and slices
// out the four scoring windows: 6m / 2y / 5y / 10y.
//
// Inputs:
//   closes / timestamps - ascending, same length, equal-cadence bars
//   params - interval & warmup config
//
// Behaviour:
//   - "10y" is the entire input (no human-imposed length cap, per spec).
//   - "5y" / "2y" / "6m" are aligned to the LAST bar and span 1825 / 730 /
//     183 calendar days respectively, then prefixed with up to WarmupBars
//     bars of warmup history. If the input is shorter than the requested
//     window, the window receives whatever's available (callers should
//     check window.Closes length before relying on it).
//
// Returns windows in ASCENDING bar count (6m → 10y) so callers iterating
// short-to-long see the cheapest evaluation first; this matches the
// progress-monitoring intent (NOT a short-circuit — see spec §3.4).
func BuildCrucibleWindows(closes []float64, timestamps []int64, params CrucibleParams) ([]CrucibleWindow, error) {
	if len(closes) != len(timestamps) {
		return nil, fmt.Errorf("crucible: closes (%d) != timestamps (%d)", len(closes), len(timestamps))
	}
	if len(closes) < 2 {
		return nil, fmt.Errorf("crucible: need at least 2 bars, got %d", len(closes))
	}
	if params.IntervalHours <= 0 {
		return nil, fmt.Errorf("crucible: IntervalHours must be > 0")
	}

	barsPerDay := 24 / params.IntervalHours
	if barsPerDay < 1 {
		barsPerDay = 1
	}

	// Each spec entry: (label, weight, eval-region days). 10y has -1 days
	// to signal "full history".
	specs := []struct {
		label  string
		weight float64
		days   int
	}{
		{"6m", 0.10, 183},
		{"2y", 0.20, 730},
		{"5y", 0.30, 1825},
		{"10y", 0.40, -1},
	}

	out := make([]CrucibleWindow, 0, len(specs))
	for _, s := range specs {
		w := makeWindow(closes, timestamps, s.label, s.weight, s.days, barsPerDay, params.WarmupBars)
		out = append(out, w)
	}
	return out, nil
}

// makeWindow extracts one window. evalDays = -1 means "use everything".
func makeWindow(closes []float64, timestamps []int64, label string, weight float64,
	evalDays, barsPerDay, warmup int) CrucibleWindow {

	n := len(closes)
	var evalStartIdx int
	if evalDays < 0 {
		// 10y / full: the entire series is eval region (no warmup prefix
		// because there's nothing earlier to use). EvalStartMs = first bar.
		evalStartIdx = 0
	} else {
		evalBars := evalDays * barsPerDay
		if evalBars >= n {
			evalStartIdx = 0
		} else {
			evalStartIdx = n - evalBars
		}
	}

	// Add warmup prefix where possible.
	startIdx := evalStartIdx - warmup
	if startIdx < 0 {
		startIdx = 0
	}

	// EvalStartMs is the timestamp of the first eval-region bar (or the
	// first bar of the slice if there's no warmup prefix available).
	evalStartMs := timestamps[evalStartIdx]

	return CrucibleWindow{
		Label:       label,
		Weight:      weight,
		Closes:      closes[startIdx:],
		Timestamps:  timestamps[startIdx:],
		EvalStartMs: evalStartMs,
	}
}
