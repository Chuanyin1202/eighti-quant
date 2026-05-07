package quant

import "math"

// Sigmoid is the standard logistic function 1 / (1 + e^x).
//
// Note the sign convention used by the dynamic-balance engine:
//   x ↑   ⇒  Sigmoid(x) ↓     (smaller target weight)
//   x = 0 ⇒  Sigmoid(x) = 0.5 (neutral)
//   x ↓   ⇒  Sigmoid(x) ↑     (larger target weight)
//
// Strategies that interpret the input as a "go-bearish" signal will
// therefore want positive Signal to lower the target weight (sell side).
//
// Edge cases: extreme positive x can overflow exp; we clamp to ±50 which
// gives Sigmoid values within 1e-22 of the asymptotes — far beyond the
// precision we ever compare against.
func Sigmoid(x float64) float64 {
	if x > 50 {
		return 0
	}
	if x < -50 {
		return 1
	}
	return 1.0 / (1.0 + math.Exp(x))
}

// DynamicBalanceInput is the parameter pack for ComputeDynamicBalance.
//
// Strategies fill in Signal as their composed scalar (e.g. PVA-weighted)
// and supply the Beta/Gamma chromosome values plus a regime-supplied Beta
// multiplier. The function does not know what the inputs mean — it just
// applies the iron formula.
type DynamicBalanceInput struct {
	Signal             float64 // composed market signal (positive = bearish convention)
	Beta               float64 // Sigmoid sensitivity (chromosome)
	BetaMultiplier     float64 // regime-driven Beta scaling (≥0)
	Gamma              float64 // inventory bias coefficient (chromosome)
	CurrentMicroWeight float64 // (FloatAsset × Price) / TotalEquity ∈ [0,1]
	TotalEquity        float64 // USDT
}

// DynamicBalanceOutput is the result of one balance evaluation.
type DynamicBalanceOutput struct {
	EffectiveBeta  float64
	InventoryBias  float64
	Exponent       float64
	TargetWeight   float64
	DeltaWeight    float64
	TheoreticalUSD float64 // > 0 = BUY, < 0 = SELL
}

// ComputeDynamicBalance applies the Sigmoid dynamic-balance formula:
//
//	EffectiveBeta = max(0.01, Beta × BetaMultiplier)
//	InventoryBias = clamp(CurrentMicroWeight, 0, 1) - 0.5
//	Exponent      = EffectiveBeta × Signal + Gamma × InventoryBias
//	TargetWeight  = clamp(Sigmoid(Exponent), 0, 1)
//	DeltaWeight   = TargetWeight - CurrentMicroWeight
//	TheoreticalUSD = DeltaWeight × TotalEquity
//
// EffectiveBeta has a hard floor of 0.01 so that a zero/near-zero
// BetaMultiplier from the regime never collapses the Sigmoid to a flat
// 0.5 (which would freeze the strategy at neutral weight).
func ComputeDynamicBalance(in DynamicBalanceInput) DynamicBalanceOutput {
	effBeta := in.Beta * in.BetaMultiplier
	if effBeta < 0.01 {
		effBeta = 0.01
	}
	invBias := ClipFloat64(in.CurrentMicroWeight, 0, 1) - 0.5
	exponent := effBeta*in.Signal + in.Gamma*invBias
	target := ClipFloat64(Sigmoid(exponent), 0, 1)
	delta := target - in.CurrentMicroWeight
	return DynamicBalanceOutput{
		EffectiveBeta:  effBeta,
		InventoryBias:  invBias,
		Exponent:       exponent,
		TargetWeight:   target,
		DeltaWeight:    delta,
		TheoreticalUSD: delta * in.TotalEquity,
	}
}
