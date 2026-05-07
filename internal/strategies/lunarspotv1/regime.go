package lunarspotv1

import (
	"math"

	"github.com/Chuanyin1202/eighti-quant/internal/quant/signals"
)

// PVA holds the three pure physical quantities driving the strategy.
type PVA struct {
	P float64 // position (deviation z-score from EMA)
	V float64 // velocity (mean log return)
	A float64 // acceleration (Δvelocity)
}

// SJM is the three-state regime label.
//
// Strings match docs/strategies/lunar-spot-v1.md §5; UI translation table
// in docs/00-架構總覽.md §8.
type SJM string

const (
	SJMPanic SJM = "panic"
	SJMGreed SJM = "greed"
	SJMQuiet SJM = "quiet"
)

// Regime is the SJM label plus the engine-modifier values it implies.
//
// Modifiers semantics (matches §5):
//   TimeDilation     — applied to macro per-tick budget growth
//   BetaMultiplier   — applied to micro Sigmoid Beta
//   IsQuiet          — true ⇒ micro wedge dust orders are zeroed
type Regime struct {
	State          SJM
	TimeDilation   float64
	BetaMultiplier float64
	IsQuiet        bool
}

// computePVA evaluates the three physical quantities given a close series
// and the chromosome-controlled lookback windows.
//
// Returns NaN-filled PVA if any building block returned NaN (insufficient
// data). The caller's Step() warm-up gate (MinWarmupBars) should prevent
// this in practice; we surface NaN rather than zero to make warmup-related
// surprises detectable in diagnostics.
func computePVA(closes []float64, c Chromosome) PVA {
	p := signals.EMABaseDeviation{
		EMABars:     c.EMABaseBars,
		SigmaWindow: c.SigmaWindow,
		SigmaFloor:  c.SigmaFloor,
	}.Value(closes)

	v := signals.LogReturnVelocity{Lookback: c.VelocityLookback}.Value(closes)
	a := signals.LogReturnAcceleration{Lookback: c.VelocityLookback}.Value(closes)

	return PVA{P: p, V: v, A: a}
}

// classifySJM compresses the PVA tuple into a regime label.
//
// Heuristics (matches docs §5):
//   panic  — A < -panicSigmaMul × σ(A)         strong downside acceleration
//   greed  — V > 0 AND A > 0                   sustained, accelerating up
//   quiet  — everything else                   the default "junk time"
//
// We use σ(A) computed from a running-window estimate (the closes-derived
// log-return acceleration's own dispersion). For Phase 4c we approximate
// σ(A) using the SigmaWindow chromosome value via a lightweight estimator
// — robust enough for demo, can be tightened in Phase 5+ if GA fitness
// shows it matters.
//
// The PanicSigmaMul constant is hard-coded at 2.0 here. Promoting it to
// a chromosome field is straightforward but introduces another evolution
// dimension; the spec keeps it static for the v1 reference.
const panicSigmaMul = 2.0

func classifySJM(closes []float64, pva PVA, c Chromosome) Regime {
	// σ(A) approximation: use the standard deviation of velocity over the
	// SigmaWindow. Acceleration is the difference of two velocity windows;
	// its scale is roughly the same as the velocity's volatility.
	logRet := computeLogReturns(closes)
	if logRet == nil {
		return Regime{State: SJMQuiet, TimeDilation: 1.0, BetaMultiplier: 0.5, IsQuiet: true}
	}

	sigmaA := stdDevTail(logRet, c.SigmaWindow)
	if math.IsNaN(sigmaA) || sigmaA <= 0 {
		return Regime{State: SJMQuiet, TimeDilation: 1.0, BetaMultiplier: 0.5, IsQuiet: true}
	}

	switch {
	case !math.IsNaN(pva.A) && pva.A < -panicSigmaMul*sigmaA:
		return Regime{State: SJMPanic, TimeDilation: 1.5, BetaMultiplier: 2.5, IsQuiet: false}

	case !math.IsNaN(pva.V) && pva.V > 0 && !math.IsNaN(pva.A) && pva.A > 0:
		return Regime{State: SJMGreed, TimeDilation: 1.0, BetaMultiplier: 1.0, IsQuiet: false}

	default:
		return Regime{State: SJMQuiet, TimeDilation: 1.0, BetaMultiplier: 0.5, IsQuiet: true}
	}
}

// computeLogReturns / stdDevTail are local light wrappers — we don't use
// quant.LogReturns + quant.StdDev directly because they have stricter
// pre-conditions (NaN on edge), and we need the regime classifier to
// gracefully degrade to "quiet" instead of panicking.

func computeLogReturns(closes []float64) []float64 {
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

func stdDevTail(x []float64, window int) float64 {
	if window < 2 || len(x) < window {
		return math.NaN()
	}
	tail := x[len(x)-window:]
	var mean float64
	for _, v := range tail {
		mean += v
	}
	mean /= float64(window)
	var sq float64
	for _, v := range tail {
		d := v - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(window-1))
}
