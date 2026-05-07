package ga

import (
	"context"
	"math/rand"

	"github.com/Chuanyin1202/eighti-quant/internal/strategies/grid"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// GridEvolvable adapts the grid strategy to the EvolvableStrategy interface.
//
// This file is the ONLY place inside internal/saas/ga that imports
// internal/strategies/grid — the rest of the engine remains strategy-blind.
// Adding a new evolvable strategy means writing one new <name>_evolvable.go
// file here without touching engine.go / fitness.go / simulator.go.
//
// Gene shape: Gene = grid.Params (5 evolvable fields). Grid has no separate
// SpawnPoint — the per-instance configuration IS the chromosome.
type GridEvolvable struct {
	strat *grid.Strategy
}

// NewGridEvolvable constructs the adapter. The underlying strategy is created
// internally; callers don't pass one in (the strategy registry already has it).
func NewGridEvolvable() *GridEvolvable {
	return &GridEvolvable{strat: &grid.Strategy{}}
}

func (g *GridEvolvable) StrategyID() string { return grid.StrategyID }

func (g *GridEvolvable) Sample(rng *rand.Rand) Gene {
	return grid.Sample(rng)
}

func (g *GridEvolvable) Mutate(gene Gene, prob, scale float64, rng *rand.Rand) Gene {
	return grid.Mutate(gene.(grid.Params), prob, scale, rng)
}

func (g *GridEvolvable) Crossover(p1, p2 Gene, rng *rand.Rand) Gene {
	return grid.Crossover(p1.(grid.Params), p2.(grid.Params), rng)
}

func (g *GridEvolvable) Fingerprint(gene Gene) uint64 {
	return grid.Fingerprint(gene.(grid.Params))
}

func (g *GridEvolvable) DecodeElite(paramPackJSON []byte) Gene {
	if len(paramPackJSON) == 0 {
		return g.strat.DefaultParams().(grid.Params)
	}
	p, err := grid.DecodeParams(paramPackJSON)
	if err != nil {
		// Corrupt basin row → fall back to default seed. Better than panicking
		// the entire epoch.
		return g.strat.DefaultParams().(grid.Params)
	}
	return p
}

func (g *GridEvolvable) EncodeResult(gene Gene, _ SpawnPoint) []byte {
	// Grid ignores SpawnPoint — the chromosome carries everything.
	buf, err := grid.EncodeParams(gene.(grid.Params))
	if err != nil {
		return nil
	}
	return buf
}

// Evaluate runs the gene against every Crucible window and aggregates the score.
//
// SimConfig is sourced from the EvaluablePlan + a few static defaults that
// don't matter for grid (it has no monthly inject — the simulator's monthly
// inject is set to 0 so cross-month adds nothing).
func (g *GridEvolvable) Evaluate(ctx context.Context, gene Gene, plan EvaluablePlan) EvaluateResult {
	cfg := DefaultSimConfig()
	cfg.InitialCapitalUSDT = 1000     // demo seed; matches grid's TotalEquity assumptions
	cfg.MonthlyInjectUSDT = 0         // grid has no recurring inject
	cfg.MinOrderUSDT = plan.MinOrderUSDT
	cfg.LotStepSize = plan.LotStep
	cfg.LotMinQty = plan.LotMin

	// Per-window backtest.
	fitnessCfg := DefaultFitnessConfig()
	sliceScores := make([]float64, len(plan.Windows))
	sliceMetrics := make([]SliceMetrics, len(plan.Windows))
	maxAcrossWindows := 0.0

	params := strategy.Params(gene.(grid.Params))

	for i, w := range plan.Windows {
		// Cancellation cooperative — each window is fast enough not to need
		// fine-grained checks, but at boundaries we honour ctx.
		if err := ctx.Err(); err != nil {
			break
		}

		res := SimulateStrategy(g.strat, params, w.Closes, w.Timestamps, w.EvalStartMs, cfg)
		base := plan.DCABaselines[i]
		score := SliceScore(w.Label, res.ROI, res.MaxDrawdown, base.ROI, base.MaxDrawdown, fitnessCfg)
		sliceScores[i] = score
		sliceMetrics[i] = SliceMetrics{Label: w.Label, ROI: res.ROI, MaxDrawdown: res.MaxDrawdown}
		if res.MaxDrawdown > maxAcrossWindows {
			maxAcrossWindows = res.MaxDrawdown
		}
	}

	return EvaluateResult{
		ScoreTotal:  ScoreTotal(plan.Windows, sliceScores),
		Windows:     sliceMetrics,
		MaxDrawdown: maxAcrossWindows,
	}
}

// Verify runs a full-history backtest. Phase 5 keeps it minimal — it just
// reuses the simulator without window slicing. Monte Carlo is Phase 14+.
func (g *GridEvolvable) Verify(ctx context.Context, gene Gene, _ SpawnPoint,
	bars []float64, lotStep, lotMin float64) BacktestMetrics {

	if len(bars) == 0 {
		return BacktestMetrics{}
	}
	cfg := DefaultSimConfig()
	cfg.InitialCapitalUSDT = 1000
	cfg.MonthlyInjectUSDT = 0
	cfg.LotStepSize = lotStep
	cfg.LotMinQty = lotMin

	// Synthesize timestamps assuming the strategy's manifest interval (1h for grid).
	ts := make([]int64, len(bars))
	for i := range ts {
		ts[i] = int64(i) * int64(60*60*1000)
	}

	res := SimulateStrategy(g.strat, strategy.Params(gene.(grid.Params)),
		bars, ts, ts[0], cfg)

	return BacktestMetrics{
		ROI:         res.ROI,
		MaxDrawdown: res.MaxDrawdown,
		NAVStart:    res.NAVStart,
		NAVEnd:      res.NAVEnd,
	}
}
