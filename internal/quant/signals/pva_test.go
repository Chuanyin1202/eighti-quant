package signals

import (
	"math"
	"testing"
)

func TestEMABaseDeviation_NotEnoughData(t *testing.T) {
	closes := []float64{100, 101, 102}
	sig := EMABaseDeviation{EMABars: 5, SigmaWindow: 5, SigmaFloor: 0.005}
	if got := sig.Value(closes); !math.IsNaN(got) {
		t.Fatalf("expected NaN for insufficient data, got %v", got)
	}
}

func TestEMABaseDeviation_AbovesEMAReturnsPositive(t *testing.T) {
	// Build a steadily rising series: latest close should be above EMA.
	closes := make([]float64, 100)
	for i := range closes {
		closes[i] = 100 + float64(i)*0.5
	}
	sig := EMABaseDeviation{EMABars: 20, SigmaWindow: 20, SigmaFloor: 0.0001}
	v := sig.Value(closes)
	if math.IsNaN(v) {
		t.Fatalf("unexpected NaN")
	}
	if v <= 0 {
		t.Fatalf("rising series: expected positive deviation, got %v", v)
	}
}

func TestLogReturnVelocity_PositiveOnUptrend(t *testing.T) {
	closes := make([]float64, 50)
	for i := range closes {
		closes[i] = 100.0 * math.Pow(1.001, float64(i))
	}
	v := LogReturnVelocity{Lookback: 10}.Value(closes)
	if math.IsNaN(v) {
		t.Fatalf("unexpected NaN")
	}
	if v <= 0 {
		t.Fatalf("uptrend velocity should be positive, got %v", v)
	}
}

func TestLogReturnVelocity_InsufficientReturnsNaN(t *testing.T) {
	closes := []float64{100, 101}
	v := LogReturnVelocity{Lookback: 5}.Value(closes)
	if !math.IsNaN(v) {
		t.Fatalf("expected NaN, got %v", v)
	}
}

func TestLogReturnAcceleration_PositiveOnExpansion(t *testing.T) {
	// 15 closes, 10 slow + 5 fast. With Lookback=5 the layout is:
	//   logReturns: 0..8 slow (mean ≈ 0.0005)
	//               9    boundary, fast value (~0.005)
	//               10..13 fast (mean ≈ 0.005)
	//   prev = logReturns[4:9]  → all slow
	//   now  = logReturns[9:14] → all fast
	// → acceleration ≈ 0.005 - 0.0005 ≈ 0.0045
	closes := make([]float64, 15)
	closes[0] = 100
	for i := 1; i < 10; i++ {
		closes[i] = closes[i-1] * 1.0005 // slow grow
	}
	for i := 10; i < 15; i++ {
		closes[i] = closes[i-1] * 1.005 // fast grow
	}
	a := LogReturnAcceleration{Lookback: 5}.Value(closes)
	if math.IsNaN(a) {
		t.Fatalf("unexpected NaN")
	}
	if a <= 0 {
		t.Fatalf("expanding series: expected positive acceleration, got %v", a)
	}
}

func TestLogReturnAcceleration_InsufficientReturnsNaN(t *testing.T) {
	closes := []float64{100, 101, 102, 103}
	if a := (LogReturnAcceleration{Lookback: 5}).Value(closes); !math.IsNaN(a) {
		t.Fatalf("expected NaN, got %v", a)
	}
}

func TestPVA_Determinism(t *testing.T) {
	closes := []float64{100, 101, 99, 100.5, 102, 101.5, 103, 102.8, 104, 103.7,
		104.2, 105, 104.6, 105.5, 106, 105.7, 106.4, 107, 106.8, 107.5,
		107.1, 108, 107.6, 108.3, 109}
	sig := EMABaseDeviation{EMABars: 10, SigmaWindow: 10, SigmaFloor: 0.001}
	if a, b := sig.Value(closes), sig.Value(closes); a != b {
		t.Fatalf("EMABaseDeviation not deterministic: %v vs %v", a, b)
	}
}
