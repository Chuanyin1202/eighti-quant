package grid

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// makeFlatCloses returns a length-N close series that all sit at price.
// Used as the "warm-up filler" — Step uses only the last 2 closes for
// crossing detection, so the EMA200 baseline is stable at `price`.
func makeFlatCloses(n int, price float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = price
	}
	return out
}

func baseInput(closes []float64) strategy.StrategyInput {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	ts := make([]int64, len(closes))
	for i := range ts {
		ts[i] = now - int64(len(closes)-1-i)*intervalMs
	}
	return strategy.StrategyInput{
		NowMs:           now,
		LatestBarTimeMs: now,
		Closes:          closes,
		Timestamps:      ts,
		LivePrice:       closes[len(closes)-1],
		Portfolio: strategy.PortfolioSnapshot{
			USDTBalance: 5000,
			FloatAsset:  0.05, // grid only sells from FLOAT
		},
		Symbol:      "BTCUSDT",
		LotStepSize: 0.00001,
		LotMinQty:   0.00001,
		MinOrderUSD: 10.1,
	}
}

func TestGrid_Manifest(t *testing.T) {
	s := &Strategy{}
	m := s.Manifest()
	if m.ID != StrategyID || !m.SupportsEvolution || m.MinWarmupBars != 200 {
		t.Fatalf("manifest: %+v", m)
	}
}

func TestGrid_WarmupReturnsEmpty(t *testing.T) {
	s := &Strategy{}
	in := baseInput(makeFlatCloses(50, 100))
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 0 {
		t.Fatalf("expected no intents during warm-up")
	}
}

func TestGrid_OutOfRange(t *testing.T) {
	s := &Strategy{}
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-1] = 200 // way above 1.3 × 100 = 130
	in := baseInput(closes)
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 0 {
		t.Fatalf("expected no intents when price out of range")
	}
	if out.Diagnostics["out_of_range"] != 1 {
		t.Fatalf("missing diagnostic")
	}
}

func TestGrid_DownCrossEmitsBuy(t *testing.T) {
	s := &Strategy{}
	// EMA200 ≈ 100; default grid: lower=70, upper=130, count=10 → step 6
	// Lines: 70, 76, 82, 88, 94, 100, 106, 112, 118, 124, 130
	// Set closes[-2]=101, closes[-1]=99 → crosses 100 going down → BUY
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-2] = 101
	closes[len(closes)-1] = 99
	in := baseInput(closes)
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 1 {
		t.Fatalf("want 1 intent, got %d", len(out.Intents))
	}
	if out.Intents[0].Action != "BUY" || out.Intents[0].LotType != "FLOAT" {
		t.Fatalf("unexpected intent: %+v", out.Intents[0])
	}
}

func TestGrid_UpCrossEmitsSell(t *testing.T) {
	s := &Strategy{}
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-2] = 99
	closes[len(closes)-1] = 101 // crosses 100 going up → SELL
	in := baseInput(closes)
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 1 {
		t.Fatalf("want 1 intent, got %d", len(out.Intents))
	}
	if out.Intents[0].Action != "SELL" || out.Intents[0].LotType != "FLOAT" {
		t.Fatalf("unexpected intent: %+v", out.Intents[0])
	}
	if out.Intents[0].QtyAsset <= 0 {
		t.Fatalf("non-positive sell qty: %v", out.Intents[0].QtyAsset)
	}
}

func TestGrid_OversellProtection(t *testing.T) {
	// Multi-line gap-up: cross 88, 94, 100, 106, 112 in one bar.
	// Total wanted SELL ≈ 5 × (orderUSDT/price) — but we limit FloatAsset to a
	// tiny amount so oversell guard kicks in. Sum of QtyAsset must ≤ FloatAsset.
	s := &Strategy{}
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-2] = 85  // below 88
	closes[len(closes)-1] = 115 // above 112
	in := baseInput(closes)
	in.Portfolio.FloatAsset = 0.001 // tiny inventory
	out := s.Step(in, s.DefaultParams())

	var totalSell float64
	for _, it := range out.Intents {
		if it.Action == "SELL" {
			totalSell += it.QtyAsset
		}
	}
	if totalSell > in.Portfolio.FloatAsset+1e-12 {
		t.Fatalf("oversell: total %v > FloatAsset %v", totalSell, in.Portfolio.FloatAsset)
	}
}

func TestGrid_CooldownSkipsSecondTrigger(t *testing.T) {
	s := &Strategy{}
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-2] = 101
	closes[len(closes)-1] = 99
	in := baseInput(closes)
	// Pretend grid line 5 (the 100-line) was just triggered 1 bar ago.
	in.Runtime = RuntimeState{
		LastTriggerBarTime: map[int]int64{5: in.LatestBarTimeMs - intervalMs},
		GridFillsCount:     map[int]int{},
	}
	out := s.Step(in, s.DefaultParams()) // default cooldown = 6 bars
	for _, it := range out.Intents {
		if it.Action == "BUY" {
			// The 100 line should be on cooldown — but the down-cross only
			// touches one line, so we'd expect no intents at all.
			t.Fatalf("expected no intents during cooldown, got %+v", out.Intents)
		}
	}
}

func TestGrid_Determinism(t *testing.T) {
	s := &Strategy{}
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-2] = 101
	closes[len(closes)-1] = 99
	in := baseInput(closes)
	a := s.Step(in, s.DefaultParams())
	b := s.Step(in, s.DefaultParams())
	if len(a.Intents) != len(b.Intents) {
		t.Fatalf("non-deterministic")
	}
	for i := range a.Intents {
		if a.Intents[i] != b.Intents[i] {
			t.Fatalf("intent[%d] differs: %+v vs %+v", i, a.Intents[i], b.Intents[i])
		}
	}
}

func TestGrid_RuntimeJSONRoundTrip(t *testing.T) {
	s := &Strategy{}
	closes := makeFlatCloses(202, 100)
	closes[len(closes)-2] = 101
	closes[len(closes)-1] = 99
	in := baseInput(closes)
	out := s.Step(in, s.DefaultParams())

	persisted, err := json.Marshal(out.NewRuntime)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(persisted, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Re-feed: same bar, runtime says line 5 just fired → cooldown blocks it.
	in2 := baseInput(closes)
	in2.Runtime = raw
	out2 := s.Step(in2, s.DefaultParams())
	if len(out2.Intents) != 0 {
		t.Fatalf("cooldown didn't survive JSON round-trip: %+v", out2.Intents)
	}
}

func TestClamp_EnforcesBoundsAndStructural(t *testing.T) {
	cases := []struct {
		name string
		in   Params
		want func(p Params) bool
	}{
		{
			"hard_bound_clip",
			Params{LowerPriceRel: 0.1, UpperPriceRel: 2.0, GridCount: 100, OrderUSDTRatio: 1, RebalanceCooldownBars: 100},
			func(p Params) bool {
				return p.LowerPriceRel == 0.5 && p.UpperPriceRel == 1.5 && p.GridCount == 30 &&
					p.OrderUSDTRatio == 0.05 && p.RebalanceCooldownBars == 24
			},
		},
		{
			"narrow_grid_widened",
			Params{LowerPriceRel: 0.95, UpperPriceRel: 1.05, GridCount: 10, OrderUSDTRatio: 0.02, RebalanceCooldownBars: 6},
			func(p Params) bool { return p.UpperPriceRel-p.LowerPriceRel >= 0.1-1e-9 },
		},
		{
			"int_round",
			Params{LowerPriceRel: 0.7, UpperPriceRel: 1.3, GridCount: 10, OrderUSDTRatio: 0.02, RebalanceCooldownBars: 6},
			func(p Params) bool { return p.GridCount == 10 && p.RebalanceCooldownBars == 6 },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Clamp(c.in)
			if !c.want(got) {
				t.Fatalf("Clamp produced %+v", got)
			}
		})
	}
}

func TestGrid_DefaultParamsDoNotMutateAfterClamp(t *testing.T) {
	s := &Strategy{}
	got := Clamp(s.DefaultParams().(Params))
	want := s.DefaultParams().(Params)
	if math.Abs(got.LowerPriceRel-want.LowerPriceRel) > 1e-12 ||
		math.Abs(got.UpperPriceRel-want.UpperPriceRel) > 1e-12 ||
		got.GridCount != want.GridCount ||
		math.Abs(got.OrderUSDTRatio-want.OrderUSDTRatio) > 1e-12 ||
		got.RebalanceCooldownBars != want.RebalanceCooldownBars {
		t.Fatalf("default already in canonical form should be a no-op:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestGrid_RegisteredOnInit(t *testing.T) {
	got, ok := strategy.Get(StrategyID)
	if !ok || got == nil {
		t.Fatalf("grid not registered")
	}
	if got.Manifest().ID != StrategyID {
		t.Fatalf("registry returned wrong strategy: %+v", got.Manifest())
	}
}
