// Package simpledca is the minimal reference strategy: a fixed-amount
// monthly DCA. It exists to:
//   1. Prove the Strategy SDK contract works without any GA / signals / regimes
//   2. Serve as a paper-trade baseline against more sophisticated strategies
//   3. Be the smallest readable example for users learning to write strategies
//
// Spec: docs/strategies/simple-dca.md.
package simpledca

import (
	"encoding/json"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

const StrategyID = "simple-dca"

// Strategy implements strategy.Strategy. No GA support — DefaultParams() is
// the only parameter source; the framework's cron tick uses it directly.
type Strategy struct{}

// init registers the strategy with the framework registry. Anonymous import
// of this package (`_ "...simpledca"`) is enough to wire it in.
func init() {
	strategy.Register(&Strategy{})
}

func (s *Strategy) Manifest() strategy.Manifest {
	return strategy.Manifest{
		ID:                  StrategyID,
		Name:                "簡單定投",
		Version:             "1.0.0",
		IsSpot:              true,
		SupportedSymbols:    nil, // any symbol
		RequiredDataKind:    "close-only",
		MinWarmupBars:       1,
		AggregationInterval: "1d",
		SupportsEvolution:   false,
	}
}

// Params is the strategy's user-supplied configuration. Persisted via the
// instance's SpawnPoint JSON blob; the framework hands it back into Step().
type Params struct {
	MonthlyAmountUSDT float64 `json:"monthly_amount_usdt"`
	BuyDayOfMonth     int     `json:"buy_day_of_month"` // 1..28
}

func (s *Strategy) DefaultParams() strategy.Params {
	return Params{
		MonthlyAmountUSDT: 100.0,
		BuyDayOfMonth:     1,
	}
}

// RuntimeState is the per-tick persistent state. The framework serializes it
// as JSON between ticks, so it must be JSON-stable.
type RuntimeState struct {
	LastBuyYearMonth string `json:"last_buy_year_month"` // e.g. "2026-05"
}

// decodeRuntime tolerates nil (first tick), a typed RuntimeState, or a
// generic map produced by JSON round-trip. Returning a zero value on
// unrecognised shapes is safer than panicking — a strategy bug should
// surface in tests, not break the cron tick.
func decodeRuntime(raw strategy.RuntimeState) RuntimeState {
	if raw == nil {
		return RuntimeState{}
	}
	switch v := raw.(type) {
	case RuntimeState:
		return v
	case *RuntimeState:
		if v == nil {
			return RuntimeState{}
		}
		return *v
	case map[string]any:
		// Re-serialise via JSON to catch field renames / migrations cleanly.
		buf, err := json.Marshal(v)
		if err != nil {
			return RuntimeState{}
		}
		var rs RuntimeState
		_ = json.Unmarshal(buf, &rs)
		return rs
	}
	return RuntimeState{}
}

func (s *Strategy) Step(in strategy.StrategyInput, p strategy.Params) strategy.StrategyOutput {
	params, _ := p.(Params)
	if params.MonthlyAmountUSDT <= 0 || params.BuyDayOfMonth < 1 || params.BuyDayOfMonth > 28 {
		// Invalid params should have been rejected upstream; emit nothing.
		return strategy.StrategyOutput{NewRuntime: in.Runtime}
	}

	runtime := decodeRuntime(in.Runtime)
	out := strategy.StrategyOutput{
		NewRuntime:  runtime,
		Diagnostics: map[string]float64{},
	}

	// LatestBarTimeMs is the bar's close time in UTC (see iron rule for time).
	bartime := time.UnixMilli(in.LatestBarTimeMs).UTC()
	yearMonth := bartime.Format("2006-01")
	day := bartime.Day()

	// Idempotent gate: don't buy twice in the same month.
	if runtime.LastBuyYearMonth == yearMonth {
		out.Diagnostics["already_bought_this_month"] = 1
		return out
	}

	// Only fire on the configured buy day.
	if day != params.BuyDayOfMonth {
		return out
	}

	// Capital check. The outer SpendableUSDT clamp is the authoritative one,
	// but we skip cleanly here to avoid emitting an obviously-doomed intent.
	if in.Portfolio.USDTBalance < params.MonthlyAmountUSDT {
		out.Diagnostics["skip_insufficient_usdt"] = 1
		return out
	}

	// Emit the buy and mark the month consumed.
	out.Intents = []strategy.TradeIntent{{
		Action:     "BUY",
		Engine:     "DCA",
		LotType:    "DEAD",
		AmountUSDT: params.MonthlyAmountUSDT,
	}}
	runtime.LastBuyYearMonth = yearMonth
	out.NewRuntime = runtime
	return out
}
