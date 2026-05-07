package ga

import (
	"math"
	"testing"
)

func TestSliceScore_Normal(t *testing.T) {
	cfg := DefaultFitnessConfig()
	// Strategy ROI 1.0, baseline ROI 0.5 → Alpha 0.5
	// MaxDDs equal → no penalty
	got := SliceScore("5y", 1.0, 0.5, 0.5, 0.5, cfg)
	if math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("want 0.5, got %v", got)
	}
}

func TestSliceScore_DDPenalty(t *testing.T) {
	cfg := DefaultFitnessConfig()
	// Alpha 0.5, but strategy DD exceeds baseline by 0.2 → penalty 0.3
	got := SliceScore("5y", 1.0, 0.7, 0.5, 0.5, cfg)
	want := 0.5 - 1.5*0.2
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestSliceScore_Fatal_6m(t *testing.T) {
	cfg := DefaultFitnessConfig()
	// MaxDD 0.6 > 0.5 threshold → fatal -200
	got := SliceScore("6m", 5.0, 0.6, 0.5, 0.5, cfg)
	if got != -200 {
		t.Fatalf("6m fatal: want -200, got %v", got)
	}
}

func TestSliceScore_Fatal_2y(t *testing.T) {
	cfg := DefaultFitnessConfig()
	if got := SliceScore("2y", 5.0, 0.8, 0.5, 0.5, cfg); got != -100 {
		t.Fatalf("2y fatal: want -100, got %v", got)
	}
}

func TestSliceScore_Fatal_10y(t *testing.T) {
	cfg := DefaultFitnessConfig()
	if got := SliceScore("10y", 5.0, 0.99, 0.5, 0.5, cfg); got != -10 {
		t.Fatalf("10y fatal: want -10, got %v", got)
	}
}

func TestScoreTotal_AllFatalWindows(t *testing.T) {
	cfg := DefaultFitnessConfig()
	// All fatal: weights 0.40 / 0.30 / 0.20 / 0.10
	// Penalties:  -10  / -10  / -100 / -200
	// Total = 0.40×-10 + 0.30×-10 + 0.20×-100 + 0.10×-200 = -47
	w := []CrucibleWindow{
		{Label: "10y", Weight: 0.40},
		{Label: "5y", Weight: 0.30},
		{Label: "2y", Weight: 0.20},
		{Label: "6m", Weight: 0.10},
	}
	scores := []float64{
		SliceScore("10y", 0, 0.99, 0, 0, cfg),
		SliceScore("5y", 0, 0.95, 0, 0, cfg),
		SliceScore("2y", 0, 0.95, 0, 0, cfg),
		SliceScore("6m", 0, 0.95, 0, 0, cfg),
	}
	got := ScoreTotal(w, scores)
	want := -47.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("all-fatal total: want %v, got %v", want, got)
	}
}

func TestScoreTotal_Only6mFatalIsHeavilyPenalized(t *testing.T) {
	// Recency-aware: 6m fatal alone must dominate the total even when
	// other windows are positive. Spec example: 1.0/1.0/0.5/-200 weights
	// 0.40/0.30/0.20/0.10 → 0.40+0.30+0.10-20 = -19.20.
	w := []CrucibleWindow{
		{Label: "10y", Weight: 0.40},
		{Label: "5y", Weight: 0.30},
		{Label: "2y", Weight: 0.20},
		{Label: "6m", Weight: 0.10},
	}
	scores := []float64{1.0, 1.0, 0.5, -200}
	got := ScoreTotal(w, scores)
	want := 1.0*0.40 + 1.0*0.30 + 0.5*0.20 + (-200)*0.10
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("only-6m-fatal: want %v, got %v", want, got)
	}
	if got > 0 {
		t.Fatalf("6m fatal must drive total negative even with strong long-term scores, got %v", got)
	}
}

func TestScoreTotal_AllGreen(t *testing.T) {
	cfg := DefaultFitnessConfig()
	w := []CrucibleWindow{
		{Label: "10y", Weight: 0.40},
		{Label: "5y", Weight: 0.30},
		{Label: "2y", Weight: 0.20},
		{Label: "6m", Weight: 0.10},
	}
	// no fatal — keep all DDs strictly below their fatal thresholds:
	// 6m=0.50, 2y=0.75, 5y=0.88, 10y=0.95.
	scores := []float64{
		SliceScore("10y", 1.0, 0.4, 0.0, 0.4, cfg),
		SliceScore("5y", 0.8, 0.4, 0.0, 0.4, cfg),
		SliceScore("2y", 0.5, 0.4, 0.0, 0.4, cfg),
		SliceScore("6m", 0.3, 0.3, 0.0, 0.3, cfg),
	}
	got := ScoreTotal(w, scores)
	if got <= 0 {
		t.Fatalf("all-green total should be positive, got %v", got)
	}
}
