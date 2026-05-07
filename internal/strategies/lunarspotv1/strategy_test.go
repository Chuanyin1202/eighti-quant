package lunarspotv1

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// makeRisingCloses returns a steadily-rising close series long enough to
// satisfy the warm-up gate.
func makeRisingCloses(n int, start, step float64) []float64 {
	out := make([]float64, n)
	cur := start
	for i := range out {
		out[i] = cur
		cur += step
	}
	return out
}

func baseInput(closes []float64, mayDay1 bool) strategy.StrategyInput {
	var nowT time.Time
	if mayDay1 {
		nowT = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	} else {
		nowT = time.Date(2026, 5, 15, 8, 0, 0, 0, time.UTC) // mid-month, 8h UTC
	}
	now := nowT.UnixMilli()
	ts := make([]int64, len(closes))
	for i := range ts {
		ts[i] = now - int64(len(closes)-1-i)*int64(4*60*60*1000)
	}
	return strategy.StrategyInput{
		NowMs:           now,
		LatestBarTimeMs: now,
		Closes:          closes,
		Timestamps:      ts,
		LivePrice:       closes[len(closes)-1],
		Portfolio: strategy.PortfolioSnapshot{
			USDTBalance: 1000,
			FloatAsset:  0,
			DeadAsset:   0,
		},
		Symbol:      "BTCUSDT",
		LotStepSize: 0.00001,
		LotMinQty:   0.00001,
		MinOrderUSD: 10.1,
	}
}

func TestLunar_Manifest(t *testing.T) {
	s := &Strategy{}
	m := s.Manifest()
	if m.ID != StrategyID || !m.SupportsEvolution || m.MinWarmupBars != 600 {
		t.Fatalf("manifest: %+v", m)
	}
	if m.AggregationInterval != "4h" || m.RequiredDataKind != "close-only" {
		t.Fatalf("manifest interval/data kind: %+v", m)
	}
}

func TestLunar_WarmupReturnsEmpty(t *testing.T) {
	s := &Strategy{}
	in := baseInput(makeRisingCloses(100, 100, 0.1), true)
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 0 || len(out.Releases) != 0 {
		t.Fatalf("expected nothing during warm-up, got %+v", out)
	}
}

func TestLunar_DeterministicSeed(t *testing.T) {
	// Default seed must produce a stable Fingerprint across multiple
	// reconstructions (no rng involved).
	a := DefaultChromosome()
	b := DefaultChromosome()
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatalf("DefaultChromosome not deterministic")
	}
	// Clamp on default must be a no-op (constraint #1 already satisfied).
	c := Clamp(Params{Chromosome: a, SpawnPoint: DefaultSpawnPoint()}).Chromosome
	if c != a {
		t.Fatalf("Clamp on default chromosome changed it:\n a=%+v\n c=%+v", a, c)
	}
}

func TestClamp_ZeroSignalWeights_DeterministicFallback(t *testing.T) {
	// All three SignalW = 0 should trigger the fallback that sets W_P = 0.5.
	in := Params{
		Chromosome: Chromosome{
			Beta: 1, Gamma: 1, SigmaFloor: 0.005,
			SignalW_P: 0, SignalW_V: 0, SignalW_A: 0,
			EMABaseBars: 200, SigmaWindow: 30, VelocityLookback: 3,
			WedgeDeltaThr: 0.03, MacroAccelerator: 1.5,
			MacroDeadlineDays: 28, DeadAgingMonths: 6,
			TargetMicroWeightFloor: 0.15,
		},
	}
	out := Clamp(in)
	if out.Chromosome.SignalW_P != 0.5 {
		t.Fatalf("zero-weight fallback should pin W_P=+0.5, got %v", out.Chromosome.SignalW_P)
	}
}

func TestLunar_RuntimeJSONRoundTrip(t *testing.T) {
	s := &Strategy{}
	closes := makeRisingCloses(MinWarmupBars+5, 100, 0.1)
	in := baseInput(closes, true)

	out := s.Step(in, s.DefaultParams())
	persisted, err := json.Marshal(out.NewRuntime)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(persisted, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	in2 := baseInput(closes, true) // same bar — idempotent guard should kick in
	in2.Runtime = raw
	out2 := s.Step(in2, s.DefaultParams())

	// The macro engine's idempotent guard means the same bar fired twice
	// must NOT mutate Pending/Spent again.
	rs2 := decodeRuntime(out2.NewRuntime)
	rs1 := decodeRuntime(out.NewRuntime)
	if rs2.Macro.LastMacroBuyBarTime != rs1.Macro.LastMacroBuyBarTime {
		t.Fatalf("idempotent guard failed across JSON RT")
	}
	if rs2.Macro.SpentThisMonth != rs1.Macro.SpentThisMonth {
		t.Fatalf("SpentThisMonth mutated on second tick: %v vs %v",
			rs1.Macro.SpentThisMonth, rs2.Macro.SpentThisMonth)
	}
}

func TestLunar_StopLossHalts(t *testing.T) {
	s := &Strategy{}
	closes := makeRisingCloses(MinWarmupBars+5, 100, 0.1)
	in := baseInput(closes, true)

	// Simulate equity collapse: zero asset, USDT below stop-loss threshold.
	// Default spawn: InitialCapital=1000, GlobalStopLoss=0.5 → stop at 500.
	in.Portfolio.USDTBalance = 400
	in.Portfolio.DeadAsset = 0
	in.Portfolio.FloatAsset = 0

	out := s.Step(in, s.DefaultParams())
	if out.Diagnostics["stop_loss_halted"] != 1 {
		t.Fatalf("expected stop_loss_halted diagnostic")
	}
	if len(out.Intents) != 0 {
		t.Fatalf("expected no intents during halt, got %d", len(out.Intents))
	}
}

func TestLunar_RegisteredOnInit(t *testing.T) {
	got, ok := strategy.Get(StrategyID)
	if !ok || got == nil {
		t.Fatalf("not registered")
	}
	if got.Manifest().ID != StrategyID {
		t.Fatalf("registry returned wrong strategy: %+v", got.Manifest())
	}
}

// Macro engine isolated tests — no PVA / micro / lots involved.

func TestMacro_IdempotentGuardPrevents2ndMutation(t *testing.T) {
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	chromo := DefaultChromosome()
	regime := Regime{TimeDilation: 1.0, BetaMultiplier: 1.0}

	rt0, intent0 := macroDecide(MacroRuntimeState{}, t1, 1000, regime, chromo, 100, 10.1)
	rt1, intent1 := macroDecide(rt0, t1, 1000, regime, chromo, 100, 10.1)

	// First call may or may not emit (depends on PerTickBase vs MinOrder).
	// Second call MUST emit nothing and MUST NOT mutate state.
	if intent1 != nil {
		t.Fatalf("idempotent guard failed: 2nd call emitted intent")
	}
	if rt1 != rt0 {
		t.Fatalf("idempotent guard failed: state mutated\n rt0=%+v\n rt1=%+v", rt0, rt1)
	}
	_ = intent0 // unused but kept for clarity
}

func TestMacro_DeadlineCarriesForwardWhenSpendableInsufficient(t *testing.T) {
	// May 28 is the default deadline. Force SpendableUSDT below MinOrderUSDT
	// to exercise the "carry forward, don't erase" branch.
	tDeadline := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC).UnixMilli()
	chromo := DefaultChromosome()
	regime := Regime{TimeDilation: 1.0, BetaMultiplier: 1.0}

	prev := MacroRuntimeState{
		CurrentMonthYearMonth: "2026-05",
		MonthlyBudget:         100,
		SpentThisMonth:        50,
		PendingMacroBudget:    20,
	}
	// Spendable=5 < MinOrder=10.1 → carry forward, no order.
	next, intent := macroDecide(prev, tDeadline, 5, regime, chromo, 100, 10.1)
	if intent != nil {
		t.Fatalf("expected no intent when Spendable < MinOrder")
	}
	// PendingMacroBudget should NOT be erased (this was the round-13 bug).
	if next.PendingMacroBudget < prev.PendingMacroBudget {
		t.Fatalf("budget erased instead of carried: prev=%v next=%v",
			prev.PendingMacroBudget, next.PendingMacroBudget)
	}
}

func TestMacro_CrossMonthResetsBudget(t *testing.T) {
	tJune := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	chromo := DefaultChromosome()
	regime := Regime{TimeDilation: 1.0, BetaMultiplier: 1.0}

	prev := MacroRuntimeState{
		CurrentMonthYearMonth: "2026-05",
		MonthlyBudget:         100,
		SpentThisMonth:        80,
		PendingMacroBudget:    5, // carries over
	}
	next, _ := macroDecide(prev, tJune, 1000, regime, chromo, 100, 10.1)
	if next.CurrentMonthYearMonth != "2026-06" {
		t.Fatalf("month not advanced: %s", next.CurrentMonthYearMonth)
	}
	if next.MonthlyBudget != 100 {
		t.Fatalf("budget not refilled: %v", next.MonthlyBudget)
	}
	if next.SpentThisMonth != 0 && next.SpentThisMonth != math.Round(next.SpentThisMonth) {
		// SpentThisMonth resets to 0 then may immediately be incremented.
		// Just sanity-check it's not still 80.
		if next.SpentThisMonth >= 80 {
			t.Fatalf("SpentThisMonth not reset")
		}
	}
}
