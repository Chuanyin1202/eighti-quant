package quant

import (
	"math"
	"testing"
)

func TestSigmoid_Neutral(t *testing.T) {
	if got := Sigmoid(0); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("Sigmoid(0) = %v, want 0.5", got)
	}
}

func TestSigmoid_Saturates(t *testing.T) {
	// large positive x → ~0
	if got := Sigmoid(50); got > 1e-20 {
		t.Fatalf("Sigmoid(50) saturating tail: got %v", got)
	}
	if got := Sigmoid(100); got != 0 { // clamp returns exactly 0
		t.Fatalf("Sigmoid(100) clamp: got %v, want 0", got)
	}
	// large negative x → ~1
	if got := Sigmoid(-50); 1-got > 1e-20 {
		t.Fatalf("Sigmoid(-50) saturating tail: got %v", got)
	}
	if got := Sigmoid(-100); got != 1 {
		t.Fatalf("Sigmoid(-100) clamp: got %v, want 1", got)
	}
}

// TestComputeDynamicBalance_SanityCases mirrors docs/strategies/lunar-spot-v1.md
// §6.2: the three sign-direction cases that any Sigmoid implementation MUST
// pass before a strategy that uses it can ship.
func TestComputeDynamicBalance_SanityCases(t *testing.T) {
	cases := []struct {
		name              string
		signal            float64
		curWeight         float64
		wantTargetApprox  float64 // tolerance ±0.005
		deltaSign         int     // +1 buy, -1 sell, 0 anywhere
	}{
		{
			name:             "bearish_signal_neutral_inventory_should_sell",
			signal:           +1.0,
			curWeight:        0.5,
			wantTargetApprox: 0.18,
			deltaSign:        -1,
		},
		{
			name:             "bullish_signal_neutral_inventory_should_buy",
			signal:           -1.0,
			curWeight:        0.5,
			wantTargetApprox: 0.82,
			deltaSign:        +1,
		},
		{
			name:             "no_signal_high_inventory_spring_pulls_back",
			signal:           0,
			curWeight:        0.8,
			wantTargetApprox: 0.46,
			deltaSign:        -1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := ComputeDynamicBalance(DynamicBalanceInput{
				Signal:             c.signal,
				Beta:               1.5,
				BetaMultiplier:     1.0,
				Gamma:              0.5,
				CurrentMicroWeight: c.curWeight,
				TotalEquity:        1000,
			})
			if math.Abs(out.TargetWeight-c.wantTargetApprox) > 0.005 {
				t.Fatalf("TargetWeight: want ~%v, got %v", c.wantTargetApprox, out.TargetWeight)
			}
			switch c.deltaSign {
			case +1:
				if out.DeltaWeight <= 0 {
					t.Fatalf("expected positive Delta (buy), got %v", out.DeltaWeight)
				}
				if out.TheoreticalUSD <= 0 {
					t.Fatalf("expected positive USD (buy), got %v", out.TheoreticalUSD)
				}
			case -1:
				if out.DeltaWeight >= 0 {
					t.Fatalf("expected negative Delta (sell), got %v", out.DeltaWeight)
				}
				if out.TheoreticalUSD >= 0 {
					t.Fatalf("expected negative USD (sell), got %v", out.TheoreticalUSD)
				}
			}
		})
	}
}

func TestComputeDynamicBalance_EffectiveBetaFloor(t *testing.T) {
	// Even with BetaMultiplier=0, EffectiveBeta should not drop below 0.01,
	// otherwise the Sigmoid collapses to flat 0.5 + gamma*invBias.
	out := ComputeDynamicBalance(DynamicBalanceInput{
		Signal:             1.0,
		Beta:               1.0,
		BetaMultiplier:     0,
		Gamma:              0,
		CurrentMicroWeight: 0.5,
		TotalEquity:        1000,
	})
	if out.EffectiveBeta != 0.01 {
		t.Fatalf("EffectiveBeta floor: want 0.01, got %v", out.EffectiveBeta)
	}
	// With Beta=0.01 × Signal=1 and Gamma=0, exponent = 0.01, target ~0.4975
	if math.Abs(out.TargetWeight-0.4975) > 1e-3 {
		t.Fatalf("TargetWeight near 0.5 expected, got %v", out.TargetWeight)
	}
}

func TestComputeDynamicBalance_Determinism(t *testing.T) {
	in := DynamicBalanceInput{
		Signal: 0.42, Beta: 1.3, BetaMultiplier: 1.1, Gamma: 0.7,
		CurrentMicroWeight: 0.35, TotalEquity: 12345.67,
	}
	a := ComputeDynamicBalance(in)
	b := ComputeDynamicBalance(in)
	if a != b {
		t.Fatalf("not deterministic: %+v vs %+v", a, b)
	}
}
