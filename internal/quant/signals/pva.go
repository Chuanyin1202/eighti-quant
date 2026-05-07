package signals

import (
	"math"

	"github.com/Chuanyin1202/eighti-quant/internal/quant"
)

// PVA stands for Position / Velocity / Acceleration — the three pure
// physical quantities used by lunar-spot-v1 and any strategy that opts into
// the same convention.
//
// Sign convention (matches Sigmoid go-bearish-positive):
//   P > 0 ⇒ price above EMA (overheated, lean bearish)
//   V > 0 ⇒ price has been rising recently (momentum up)
//   A > 0 ⇒ recent rises are accelerating
//
// The kernel does not assume a particular sign meaning; the caller's
// SignalW_* coefficients steer how each gets composed.

// EMABaseDeviation returns the z-score-style deviation of the latest close
// from an EMA baseline, normalized by sigma:
//
//	value = (close - EMA(closes, EMABars)) / max(sigma, sigmaFloor)
//
// EMABars and SigmaWindow are both chromosome candidates. SigmaFloor is
// usually a small positive (e.g. 0.005 of price) to prevent divide-by-near-
// zero blow-ups during volatility crunches.
type EMABaseDeviation struct {
	EMABars     int
	SigmaWindow int
	SigmaFloor  float64
}

func (e EMABaseDeviation) Value(closes []float64) float64 {
	if len(closes) == 0 || e.EMABars < 1 || e.SigmaWindow < 2 {
		return math.NaN()
	}
	if len(closes) < e.EMABars || len(closes) < e.SigmaWindow+1 {
		return math.NaN()
	}
	ema := quant.EMA(closes, e.EMABars)
	if math.IsNaN(ema) {
		return math.NaN()
	}
	// Use log-returns sigma rather than raw-price sigma to keep the scalar
	// dimensionless (ratio rather than USDT). Caller can also pass raw closes
	// if they accept the unit drift; we choose the more robust form here.
	logReturns := quant.LogReturns(closes)
	if logReturns == nil {
		return math.NaN()
	}
	if len(logReturns) < e.SigmaWindow {
		return math.NaN()
	}
	sigma := quant.StdDev(logReturns, e.SigmaWindow)
	if math.IsNaN(sigma) {
		return math.NaN()
	}
	denom := sigma
	if denom < e.SigmaFloor {
		denom = e.SigmaFloor
	}
	last := closes[len(closes)-1]
	return (last - ema) / (last * denom) // (price - EMA) / (price · sigma)
}

// LogReturnVelocity returns a smoothed first derivative of the log-price
// series:
//
//	value = mean(LogReturns(closes)[-Lookback:])
//
// "Lookback" is in bars. A higher value smooths but lags; the caller's
// chromosome can tune it.
type LogReturnVelocity struct {
	Lookback int
}

func (v LogReturnVelocity) Value(closes []float64) float64 {
	if v.Lookback < 1 {
		return math.NaN()
	}
	logReturns := quant.LogReturns(closes)
	if logReturns == nil || len(logReturns) < v.Lookback {
		return math.NaN()
	}
	tail := logReturns[len(logReturns)-v.Lookback:]
	var sum float64
	for _, r := range tail {
		sum += r
	}
	return sum / float64(v.Lookback)
}

// LogReturnAcceleration returns the change in velocity between the most
// recent window and the prior window:
//
//	value = velocity_now - velocity_prev
//
// where velocity_now is over the last Lookback bars and velocity_prev is
// over the Lookback bars before that. Captures whether momentum is
// accelerating (positive) or decaying (negative).
type LogReturnAcceleration struct {
	Lookback int
}

func (a LogReturnAcceleration) Value(closes []float64) float64 {
	if a.Lookback < 1 {
		return math.NaN()
	}
	logReturns := quant.LogReturns(closes)
	if logReturns == nil || len(logReturns) < 2*a.Lookback {
		return math.NaN()
	}
	mean := func(xs []float64) float64 {
		var s float64
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	}
	now := mean(logReturns[len(logReturns)-a.Lookback:])
	prev := mean(logReturns[len(logReturns)-2*a.Lookback : len(logReturns)-a.Lookback])
	return now - prev
}
