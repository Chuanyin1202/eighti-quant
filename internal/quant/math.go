// Package quant holds strategy-agnostic quantitative primitives. All exported
// functions are pure (no I/O, no clock, no goroutines). Strategies and
// building blocks compose these to express their domain logic.
package quant

import "math"

// EMA returns the exponential moving average of the last `period` values
// of x. The smoothing factor is 2/(period+1), the standard EMA convention.
//
// Bootstrap: the first EMA is the simple average of x[0..period-1]; subsequent
// values fold in the standard alpha smoothing. Returns NaN if x has fewer
// than `period` samples or period < 1 (caller's responsibility to check).
func EMA(x []float64, period int) float64 {
	if period < 1 || len(x) < period {
		return math.NaN()
	}
	alpha := 2.0 / float64(period+1)
	// Seed: SMA over the first `period` samples.
	var sum float64
	for i := 0; i < period; i++ {
		sum += x[i]
	}
	ema := sum / float64(period)
	for i := period; i < len(x); i++ {
		ema = alpha*x[i] + (1-alpha)*ema
	}
	return ema
}

// StdDev returns the sample standard deviation of the last `window` values
// of x. Uses the unbiased estimator (n-1 in the denominator).
//
// Returns NaN if window < 2 or len(x) < window.
func StdDev(x []float64, window int) float64 {
	if window < 2 || len(x) < window {
		return math.NaN()
	}
	tail := x[len(x)-window:]
	var mean float64
	for _, v := range tail {
		mean += v
	}
	mean /= float64(window)
	var sumSq float64
	for _, v := range tail {
		d := v - mean
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(window-1))
}

// LogReturns returns the per-bar log returns derived from a close series.
// Output length is len(closes)-1; output[i] = ln(closes[i+1]/closes[i]).
//
// Returns nil if len(closes) < 2 or any close <= 0.
func LogReturns(closes []float64) []float64 {
	if len(closes) < 2 {
		return nil
	}
	out := make([]float64, len(closes)-1)
	for i := 0; i < len(closes)-1; i++ {
		if closes[i] <= 0 || closes[i+1] <= 0 {
			return nil
		}
		out[i] = math.Log(closes[i+1] / closes[i])
	}
	return out
}

// MAVAbsChange returns the average absolute change between consecutive closes
// over the last `period` samples. Unlike ATR, it ignores High/Low; it is the
// equivalent of "average |close[t]-close[t-1]|" over the window.
//
// Useful for the wedge filter's volatility ratio (short-window ÷ long-window).
// Returns NaN if period < 2 or len(closes) <= period.
func MAVAbsChange(closes []float64, period int) float64 {
	if period < 2 || len(closes) <= period {
		return math.NaN()
	}
	tail := closes[len(closes)-period-1:] // need period+1 to compute period diffs
	var sum float64
	for i := 1; i < len(tail); i++ {
		sum += math.Abs(tail[i] - tail[i-1])
	}
	return sum / float64(period)
}

// ClipFloat64 clamps v to the closed interval [lo, hi].
// If lo > hi the function panics — callers shouldn't pass an inverted range.
func ClipFloat64(v, lo, hi float64) float64 {
	if lo > hi {
		panic("quant: ClipFloat64 with lo > hi")
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// RoundToUSDT rounds v to two decimal places using banker's rounding-style
// half-away-from-zero (math.Round behavior).
func RoundToUSDT(v float64) float64 {
	return math.Round(v*100) / 100
}
