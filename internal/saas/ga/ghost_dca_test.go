package ga

import "testing"

// makeFlatSeries produces n bars at constant `price`, with 4h spacing.
func makeFlatSeries(n int, price float64) ([]float64, []int64) {
	closes := make([]float64, n)
	ts := make([]int64, n)
	for i := range closes {
		closes[i] = price
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	return closes, ts
}

func TestGhostDCA_FlatPriceZeroROI(t *testing.T) {
	closes, ts := makeFlatSeries(200, 100)
	params := GhostDCAParams{
		InitialCapitalUSDT: 1000,
		MonthlyInjectUSDT:  100,
		MinOrderUSDT:       10.1,
		IntervalHours:      4,
		DeadlineDays:       28,
	}
	roi, dd := SimulateGhostDCA(closes, ts, ts[0], params)
	// Flat price → asset doesn't appreciate. Modified Dietz says ROI ≈ 0.
	// Some tiny float noise OK.
	if roi > 0.01 || roi < -0.01 {
		t.Fatalf("flat price ROI should be ~0, got %v", roi)
	}
	if dd != 0 {
		t.Fatalf("flat price DD should be 0, got %v", dd)
	}
}

func TestGhostDCA_RisingPricePositiveROI(t *testing.T) {
	closes := make([]float64, 1000)
	ts := make([]int64, 1000)
	for i := range closes {
		closes[i] = 100.0 * (1.0 + float64(i)*0.0005) // gentle uptrend
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	params := GhostDCAParams{
		InitialCapitalUSDT: 1000,
		MonthlyInjectUSDT:  100,
		MinOrderUSDT:       10.1,
		IntervalHours:      4,
		DeadlineDays:       28,
	}
	roi, _ := SimulateGhostDCA(closes, ts, ts[0], params)
	if roi <= 0 {
		t.Fatalf("rising series should produce positive ROI, got %v", roi)
	}
}

func TestGhostDCA_DrawdownDetected(t *testing.T) {
	// Up then down: peak at ~idx 200, then drops 50%.
	closes := make([]float64, 400)
	ts := make([]int64, 400)
	for i := range closes {
		var price float64
		if i < 200 {
			price = 100.0 + float64(i)
		} else {
			price = 300.0 - float64(i-200)
		}
		closes[i] = price
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	params := GhostDCAParams{
		InitialCapitalUSDT: 1000,
		MonthlyInjectUSDT:  100,
		MinOrderUSDT:       10.1,
		IntervalHours:      4,
		DeadlineDays:       28,
	}
	_, dd := SimulateGhostDCA(closes, ts, ts[0], params)
	if dd <= 0.1 {
		t.Fatalf("expected sizeable drawdown, got %v", dd)
	}
}

func TestMaxDrawdown(t *testing.T) {
	nav := []float64{100, 120, 90, 100, 80, 90}
	// Peak 120 → trough 80 → DD = (120-80)/120 = 0.333...
	got := MaxDrawdown(nav)
	want := 0.3333333333
	if got < want-0.01 || got > want+0.01 {
		t.Fatalf("MaxDrawdown: want ~%v, got %v", want, got)
	}
}

func TestMaxDrawdown_NoLossSeries(t *testing.T) {
	nav := []float64{100, 110, 120, 130}
	if got := MaxDrawdown(nav); got != 0 {
		t.Fatalf("monotone series should have 0 DD, got %v", got)
	}
}
