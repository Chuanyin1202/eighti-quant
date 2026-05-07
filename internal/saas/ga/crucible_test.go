package ga

import "testing"

func TestBuildCrucible_FourWindows(t *testing.T) {
	// 5000 bars of 4h ≈ 833 days; enough for 6m / 2y / 5y.
	closes := make([]float64, 5000)
	ts := make([]int64, 5000)
	for i := range closes {
		closes[i] = 100.0
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	w, err := BuildCrucibleWindows(closes, ts, DefaultCrucibleParams())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(w) != 4 {
		t.Fatalf("want 4 windows, got %d", len(w))
	}
	wantLabels := []string{"6m", "2y", "5y", "10y"}
	wantWeights := []float64{0.10, 0.20, 0.30, 0.40}
	for i, win := range w {
		if win.Label != wantLabels[i] {
			t.Fatalf("[%d] label: want %s, got %s", i, wantLabels[i], win.Label)
		}
		if win.Weight != wantWeights[i] {
			t.Fatalf("[%d] weight: want %v, got %v", i, wantWeights[i], win.Weight)
		}
		if len(win.Closes) == 0 {
			t.Fatalf("[%d] empty closes", i)
		}
		// EvalStartMs may be 0 if the window's eval region begins at the
		// first bar (input shorter than the requested span, or 10y window).
		_ = win.EvalStartMs
	}
}

func TestBuildCrucible_10yIsFullSeries(t *testing.T) {
	closes := make([]float64, 100)
	ts := make([]int64, 100)
	for i := range closes {
		closes[i] = 100
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	w, _ := BuildCrucibleWindows(closes, ts, DefaultCrucibleParams())
	tenY := w[3]
	if len(tenY.Closes) != 100 {
		t.Fatalf("10y must contain full series, got %d", len(tenY.Closes))
	}
}

func TestBuildCrucible_ShortSeriesGracefullyDegrades(t *testing.T) {
	closes := []float64{100, 101, 102, 103}
	ts := []int64{0, 1, 2, 3}
	w, err := BuildCrucibleWindows(closes, ts, CrucibleParams{IntervalHours: 4, WarmupBars: 100})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(w) != 4 {
		t.Fatalf("want 4 windows even on tiny series")
	}
	// All windows should contain the full 4 bars (warmup ate everything).
	for _, win := range w {
		if len(win.Closes) != 4 {
			t.Fatalf("[%s] want 4, got %d", win.Label, len(win.Closes))
		}
	}
}

func TestBuildCrucible_RejectsMismatchedLengths(t *testing.T) {
	if _, err := BuildCrucibleWindows([]float64{1, 2}, []int64{1}, DefaultCrucibleParams()); err == nil {
		t.Fatalf("expected error on mismatched lengths")
	}
}
