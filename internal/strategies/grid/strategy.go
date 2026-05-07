// Package grid is a mid-complexity reference strategy: equidistant range
// trading with GA-evolvable parameters.
//
// Spec: docs/strategies/grid.md.
//
// Design choices worth flagging up-front:
//   - close-only data (Manifest.RequiredDataKind="close-only") with explicit
//     limitations documented in the spec §2.2 — intrabar wicks don't trigger
//   - oversell protection (§2.4) tracks remaining inventory inside Step()
//     so multi-line gap-up crossings can never exceed FloatAsset
//   - grid does NOT use hard_release; if FloatAsset can't satisfy a SELL,
//     the SELL is simply skipped or capped (no DEAD lot borrowing)
package grid

import (
	"encoding/json"
	"math"

	"github.com/Chuanyin1202/eighti-quant/internal/quant"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

const StrategyID = "grid"

// MinWarmupBars must equal the longest indicator window used by Step().
// Currently we use EMA(200), so 200 is the floor.
const MinWarmupBars = 200

type Strategy struct{}

func init() {
	strategy.Register(&Strategy{})
}

func (s *Strategy) Manifest() strategy.Manifest {
	return strategy.Manifest{
		ID:                  StrategyID,
		Name:                "網格交易",
		Version:             "1.0.0",
		IsSpot:              true,
		SupportedSymbols:    nil,
		RequiredDataKind:    "close-only",
		MinWarmupBars:       MinWarmupBars,
		AggregationInterval: "1h",
		SupportsEvolution:   true,
	}
}

// Params is the chromosome. All five fields are GA-evolvable.
//
// LowerPriceRel / UpperPriceRel are EMA(200)-relative ratios so the grid
// auto-follows the trend (iron rule §2.5: dimensionless).
//
// GridCount and RebalanceCooldownBars are integer-valued; Clamp rounds them.
type Params struct {
	LowerPriceRel         float64 `json:"lower_price_rel"`
	UpperPriceRel         float64 `json:"upper_price_rel"`
	GridCount             int     `json:"grid_count"`
	OrderUSDTRatio        float64 `json:"order_usdt_ratio"`
	RebalanceCooldownBars int     `json:"rebalance_cooldown_bars"`
}

// Default values match docs/strategies/grid.md §3.
func (s *Strategy) DefaultParams() strategy.Params {
	return Params{
		LowerPriceRel:         0.7,
		UpperPriceRel:         1.3,
		GridCount:             10,
		OrderUSDTRatio:        0.02,
		RebalanceCooldownBars: 6,
	}
}

// Clamp enforces hard bounds and structural constraints on a Params value.
// Used both during init () validation and after GA Mutate/Crossover so the
// engine never receives an invalid chromosome.
//
// Hard bounds (matches grid.md §3 table):
//   LowerPriceRel         ∈ [0.5, 0.95]
//   UpperPriceRel         ∈ [1.05, 1.5]
//   GridCount             ∈ [5, 30]    integer
//   OrderUSDTRatio        ∈ [0.005, 0.05]
//   RebalanceCooldownBars ∈ [1, 24]    integer
//
// Structural constraint:
//   UpperPriceRel ≥ LowerPriceRel + 0.1   (prevents degenerate-narrow grid)
func Clamp(p Params) Params {
	p.LowerPriceRel = quant.ClipFloat64(p.LowerPriceRel, 0.5, 0.95)
	p.UpperPriceRel = quant.ClipFloat64(p.UpperPriceRel, 1.05, 1.5)
	p.OrderUSDTRatio = quant.ClipFloat64(p.OrderUSDTRatio, 0.005, 0.05)

	// Round + clamp integer fields (chromosome may carry float drift after mutation).
	gc := int(math.Round(float64(p.GridCount)))
	if gc < 5 {
		gc = 5
	} else if gc > 30 {
		gc = 30
	}
	p.GridCount = gc

	cd := int(math.Round(float64(p.RebalanceCooldownBars)))
	if cd < 1 {
		cd = 1
	} else if cd > 24 {
		cd = 24
	}
	p.RebalanceCooldownBars = cd

	// Structural: keep at least 0.1 between lower and upper.
	if p.UpperPriceRel < p.LowerPriceRel+0.1 {
		p.UpperPriceRel = p.LowerPriceRel + 0.1
		// Re-clamp upper in case the bump pushed it past its own ceiling.
		p.UpperPriceRel = quant.ClipFloat64(p.UpperPriceRel, 1.05, 1.5)
	}
	return p
}

// RuntimeState tracks per-grid-line cooldowns and fill counts.
// Persisted as JSON between ticks; map keys are stringified grid indices
// (Go's json.Marshal forces string keys for maps).
type RuntimeState struct {
	LastTriggerBarTime map[int]int64 `json:"last_trigger_bar_time"`
	GridFillsCount     map[int]int   `json:"grid_fills_count"`
}

func decodeRuntime(raw strategy.RuntimeState) RuntimeState {
	zero := RuntimeState{
		LastTriggerBarTime: map[int]int64{},
		GridFillsCount:     map[int]int{},
	}
	if raw == nil {
		return zero
	}
	switch v := raw.(type) {
	case RuntimeState:
		if v.LastTriggerBarTime == nil {
			v.LastTriggerBarTime = map[int]int64{}
		}
		if v.GridFillsCount == nil {
			v.GridFillsCount = map[int]int{}
		}
		return v
	case map[string]any:
		buf, err := json.Marshal(v)
		if err != nil {
			return zero
		}
		// JSON int-keyed maps round-trip as map[string]X — re-key here.
		var raw struct {
			LastTriggerBarTime map[string]int64 `json:"last_trigger_bar_time"`
			GridFillsCount     map[string]int   `json:"grid_fills_count"`
		}
		if err := json.Unmarshal(buf, &raw); err != nil {
			return zero
		}
		out := zero
		for k, v := range raw.LastTriggerBarTime {
			i := atoi(k)
			out.LastTriggerBarTime[i] = v
		}
		for k, v := range raw.GridFillsCount {
			i := atoi(k)
			out.GridFillsCount[i] = v
		}
		return out
	}
	return zero
}

// atoi without errors — returns 0 on bad input. Keys are strategy-controlled
// integers, so a malformed key indicates a deeper bug; falling through to 0
// is harmless because that grid line either never fired or will be replaced.
func atoi(s string) int {
	var n int
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// intervalMs is conservatively set to 1h (matches Manifest.AggregationInterval).
// If the framework ever switches a grid instance to a different interval we'd
// need to thread the interval through StrategyInput. For now the cooldown is
// expressed in "bars" and converted via this constant.
const intervalMs = int64(60 * 60 * 1000)

func (s *Strategy) Step(in strategy.StrategyInput, p strategy.Params) strategy.StrategyOutput {
	params, _ := p.(Params)
	params = Clamp(params)
	runtime := decodeRuntime(in.Runtime)

	out := strategy.StrategyOutput{
		NewRuntime:  runtime,
		Diagnostics: map[string]float64{},
	}

	// Warm-up.
	if len(in.Closes) < MinWarmupBars {
		return out
	}

	// 1. Grid geometry from EMA200 baseline.
	ema := quant.EMA(in.Closes, 200)
	if math.IsNaN(ema) || ema <= 0 {
		return out
	}
	lower := ema * params.LowerPriceRel
	upper := ema * params.UpperPriceRel
	step := (upper - lower) / float64(params.GridCount)

	closeNow := in.Closes[len(in.Closes)-1]
	closePrev := in.Closes[len(in.Closes)-2]

	// 2. Out-of-range: don't trade; surface to UI via diagnostics.
	if closeNow < lower || closeNow > upper {
		out.Diagnostics["out_of_range"] = 1
		return out
	}

	// 3. Per-cell USDT amount, clamped against MinOrderUSD.
	totalEquity := in.Portfolio.USDTBalance +
		(in.Portfolio.DeadAsset+in.Portfolio.FloatAsset+in.Portfolio.ColdSealedAsset)*in.LivePrice
	orderUSDT := totalEquity * params.OrderUSDTRatio
	if orderUSDT < in.MinOrderUSD {
		out.Diagnostics["order_below_min"] = 1
		return out
	}

	// 4. Crossing detection with oversell protection.
	// remainingFloatQty: only FLOAT inventory can be sold (grid never borrows from DEAD).
	remainingFloatQty := in.Portfolio.FloatAsset
	remainingUSDT := in.Portfolio.USDTBalance

	cooldownMs := int64(params.RebalanceCooldownBars) * intervalMs
	for i := 0; i <= params.GridCount; i++ {
		gridLine := lower + float64(i)*step

		if last := runtime.LastTriggerBarTime[i]; last != 0 {
			if in.LatestBarTimeMs-last < cooldownMs {
				continue
			}
		}

		switch {
		case closePrev > gridLine && closeNow <= gridLine:
			// Down-cross → BUY.
			if orderUSDT > remainingUSDT {
				continue
			}
			out.Intents = append(out.Intents, strategy.TradeIntent{
				Action:     "BUY",
				Engine:     "GRID",
				LotType:    "FLOAT",
				AmountUSDT: orderUSDT,
			})
			remainingUSDT -= orderUSDT
			runtime.LastTriggerBarTime[i] = in.LatestBarTimeMs
			runtime.GridFillsCount[i]++

		case closePrev < gridLine && closeNow >= gridLine:
			// Up-cross → SELL, but cap by remaining float qty (oversell guard).
			wantQty := orderUSDT / closeNow
			sellQty := wantQty
			if sellQty > remainingFloatQty {
				sellQty = remainingFloatQty
			}
			if sellQty < in.LotMinQty {
				continue
			}
			out.Intents = append(out.Intents, strategy.TradeIntent{
				Action:   "SELL",
				Engine:   "GRID",
				LotType:  "FLOAT",
				QtyAsset: sellQty,
			})
			remainingFloatQty -= sellQty
			runtime.LastTriggerBarTime[i] = in.LatestBarTimeMs
			runtime.GridFillsCount[i]++
		}
	}

	out.NewRuntime = runtime
	return out
}
