package simpledca

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// utcMs returns the millisecond timestamp for a UTC date.
func utcMs(year int, month time.Month, day, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC).UnixMilli()
}

func baseInput(barTime int64) strategy.StrategyInput {
	return strategy.StrategyInput{
		NowMs:           barTime,
		LatestBarTimeMs: barTime,
		Closes:          []float64{100},
		Timestamps:      []int64{barTime},
		LivePrice:       100,
		Portfolio:       strategy.PortfolioSnapshot{USDTBalance: 1000},
		Symbol:          "BTCUSDT",
		LotStepSize:     0.00001,
		LotMinQty:       0.00001,
		MinOrderUSD:     10.1,
	}
}

func TestSimpleDCA_Manifest(t *testing.T) {
	s := &Strategy{}
	m := s.Manifest()
	if m.ID != StrategyID || m.RequiredDataKind != "close-only" || m.SupportsEvolution {
		t.Fatalf("manifest: %+v", m)
	}
}

func TestSimpleDCA_BuysOnConfiguredDay(t *testing.T) {
	s := &Strategy{}
	in := baseInput(utcMs(2026, time.May, 1, 0))
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 1 {
		t.Fatalf("want 1 intent on day 1, got %d", len(out.Intents))
	}
	if out.Intents[0].Action != "BUY" || out.Intents[0].LotType != "DEAD" || out.Intents[0].AmountUSDT != 100 {
		t.Fatalf("unexpected intent: %+v", out.Intents[0])
	}
	rs, ok := out.NewRuntime.(RuntimeState)
	if !ok || rs.LastBuyYearMonth != "2026-05" {
		t.Fatalf("runtime not stamped: %+v", out.NewRuntime)
	}
}

func TestSimpleDCA_SkipsOnNonBuyDay(t *testing.T) {
	s := &Strategy{}
	in := baseInput(utcMs(2026, time.May, 15, 0))
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 0 {
		t.Fatalf("want 0 intents on day 15, got %d", len(out.Intents))
	}
}

func TestSimpleDCA_DoesNotDoubleBuyWithinMonth(t *testing.T) {
	s := &Strategy{}
	// Fake "already bought this month" runtime
	in := baseInput(utcMs(2026, time.May, 1, 4)) // 4h bar, still day 1
	in.Runtime = RuntimeState{LastBuyYearMonth: "2026-05"}
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 0 {
		t.Fatalf("expected 0 intents (already bought), got %d", len(out.Intents))
	}
	if out.Diagnostics["already_bought_this_month"] != 1 {
		t.Fatalf("missing diagnostic flag: %+v", out.Diagnostics)
	}
}

func TestSimpleDCA_SkipOnInsufficientFunds(t *testing.T) {
	s := &Strategy{}
	in := baseInput(utcMs(2026, time.May, 1, 0))
	in.Portfolio.USDTBalance = 50 // < 100 default
	out := s.Step(in, s.DefaultParams())
	if len(out.Intents) != 0 {
		t.Fatalf("want 0 intents when funds insufficient")
	}
	if out.Diagnostics["skip_insufficient_usdt"] != 1 {
		t.Fatalf("missing diagnostic flag")
	}
}

func TestSimpleDCA_CrossMonthBuysAgain(t *testing.T) {
	s := &Strategy{}

	// May 1 — buy.
	mayIn := baseInput(utcMs(2026, time.May, 1, 0))
	mayOut := s.Step(mayIn, s.DefaultParams())
	if len(mayOut.Intents) != 1 {
		t.Fatalf("May buy missing")
	}

	// June 1 — same runtime carried, buy again.
	juneIn := baseInput(utcMs(2026, time.June, 1, 0))
	juneIn.Runtime = mayOut.NewRuntime
	juneOut := s.Step(juneIn, s.DefaultParams())
	if len(juneOut.Intents) != 1 {
		t.Fatalf("June buy missing: %+v", juneOut)
	}
	rs := juneOut.NewRuntime.(RuntimeState)
	if rs.LastBuyYearMonth != "2026-06" {
		t.Fatalf("runtime not advanced: %+v", rs)
	}
}

func TestSimpleDCA_RuntimeJSONRoundTrip(t *testing.T) {
	// Simulate the framework persisting NewRuntime as JSON and decoding it
	// back into the strategy on the next tick.
	s := &Strategy{}

	// First tick: fresh state, buy.
	in := baseInput(utcMs(2026, time.May, 1, 0))
	out := s.Step(in, s.DefaultParams())
	persisted, err := json.Marshal(out.NewRuntime)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Decode as a generic map (what the framework will hand back).
	var raw map[string]any
	if err := json.Unmarshal(persisted, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Second tick same day: should detect "already bought".
	in2 := baseInput(utcMs(2026, time.May, 1, 4))
	in2.Runtime = raw
	out2 := s.Step(in2, s.DefaultParams())
	if len(out2.Intents) != 0 {
		t.Fatalf("JSON round-trip lost runtime: %+v", out2)
	}
	if out2.Diagnostics["already_bought_this_month"] != 1 {
		t.Fatalf("diagnostic missing after JSON round trip")
	}
}

func TestSimpleDCA_Determinism(t *testing.T) {
	s := &Strategy{}
	in := baseInput(utcMs(2026, time.May, 1, 0))
	a := s.Step(in, s.DefaultParams())
	b := s.Step(in, s.DefaultParams())
	if len(a.Intents) != len(b.Intents) || a.Intents[0] != b.Intents[0] {
		t.Fatalf("non-deterministic: %+v vs %+v", a, b)
	}
}

func TestSimpleDCA_RegisteredOnInit(t *testing.T) {
	got, ok := strategy.Get(StrategyID)
	if !ok || got == nil {
		t.Fatalf("simpledca not registered in global registry")
	}
	if got.Manifest().ID != StrategyID {
		t.Fatalf("registry returned wrong strategy: %+v", got.Manifest())
	}
}
