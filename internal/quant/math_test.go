package quant

import (
	"math"
	"testing"
)

func TestEMA_BasicConvergence(t *testing.T) {
	// Constant series: EMA must equal the constant.
	x := []float64{5, 5, 5, 5, 5, 5}
	if got := EMA(x, 3); got != 5 {
		t.Fatalf("EMA constant: want 5, got %v", got)
	}
}

func TestEMA_ShortSeries(t *testing.T) {
	if got := EMA([]float64{1, 2}, 5); !math.IsNaN(got) {
		t.Fatalf("expected NaN for len(x) < period, got %v", got)
	}
}

func TestEMA_KnownStep(t *testing.T) {
	// period=3, alpha = 2/4 = 0.5
	// seed = mean(1,2,3) = 2
	// step on 4: 0.5*4 + 0.5*2 = 3
	// step on 5: 0.5*5 + 0.5*3 = 4
	x := []float64{1, 2, 3, 4, 5}
	if got, want := EMA(x, 3), 4.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("EMA known step: want %v, got %v", want, got)
	}
}

func TestStdDev_KnownValue(t *testing.T) {
	// {2,4,4,4,5,5,7,9}: mean=5, ssd=32, sample variance = 32/7,
	// sample stddev = sqrt(32/7) ≈ 2.13808993...
	x := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	want := math.Sqrt(32.0 / 7.0)
	if got := StdDev(x, 8); math.Abs(got-want) > 1e-9 {
		t.Fatalf("StdDev: want %v, got %v", want, got)
	}
}

func TestStdDev_TooShort(t *testing.T) {
	if got := StdDev([]float64{1}, 2); !math.IsNaN(got) {
		t.Fatalf("expected NaN, got %v", got)
	}
}

func TestLogReturns_BasicShape(t *testing.T) {
	closes := []float64{100, 110, 121}
	r := LogReturns(closes)
	if len(r) != 2 {
		t.Fatalf("want len 2, got %d", len(r))
	}
	// r[0] = ln(1.1), r[1] = ln(1.1)
	want := math.Log(1.1)
	for i, v := range r {
		if math.Abs(v-want) > 1e-12 {
			t.Fatalf("r[%d]: want %v, got %v", i, want, v)
		}
	}
}

func TestLogReturns_NegativePriceReturnsNil(t *testing.T) {
	if r := LogReturns([]float64{100, -1, 50}); r != nil {
		t.Fatalf("expected nil for non-positive close, got %v", r)
	}
}

func TestMAVAbsChange(t *testing.T) {
	// closes: 1, 3, 2, 5  → diffs 2, 1, 3 → mean = 2
	closes := []float64{1, 3, 2, 5}
	if got, want := MAVAbsChange(closes, 3), 2.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("MAVAbsChange: want %v, got %v", want, got)
	}
}

func TestClipFloat64(t *testing.T) {
	cases := []struct {
		v, lo, hi, want float64
	}{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
		{3.14, 3.14, 3.14, 3.14},
	}
	for _, c := range cases {
		if got := ClipFloat64(c.v, c.lo, c.hi); got != c.want {
			t.Fatalf("Clip(%v,%v,%v): want %v, got %v", c.v, c.lo, c.hi, c.want, got)
		}
	}
}

func TestClipFloat64_PanicsOnInverted(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for lo > hi")
		}
	}()
	_ = ClipFloat64(0, 10, 5)
}

func TestRoundToUSDT(t *testing.T) {
	cases := map[float64]float64{
		1.234:    1.23,
		1.235:    1.24, // half-away-from-zero
		-1.235:   -1.24,
		100.0001: 100.00,
		99.9999:  100.00,
	}
	for in, want := range cases {
		if got := RoundToUSDT(in); math.Abs(got-want) > 1e-9 {
			t.Fatalf("RoundToUSDT(%v): want %v, got %v", in, want, got)
		}
	}
}
