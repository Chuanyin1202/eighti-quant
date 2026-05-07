package ga

import (
	"context"
	"math/rand"

	"github.com/Chuanyin1202/eighti-quant/internal/strategies/lunarspotv1"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// LunarEvolvable adapts the lunar-spot-v1 strategy to EvolvableStrategy.
//
// Gene shape: Gene = lunarspotv1.Chromosome (14 evolvable fields).
// SpawnPoint travels in EvaluablePlan.Spawn (4 user-set fields, NOT evolved).
// EncodeResult / DecodeElite assemble/disassemble the full Params pack.
type LunarEvolvable struct {
	strat *lunarspotv1.Strategy
}

func NewLunarEvolvable() *LunarEvolvable {
	return &LunarEvolvable{strat: &lunarspotv1.Strategy{}}
}

func (l *LunarEvolvable) StrategyID() string { return lunarspotv1.StrategyID }

func (l *LunarEvolvable) Sample(rng *rand.Rand) Gene {
	return lunarspotv1.Sample(rng)
}

func (l *LunarEvolvable) Mutate(gene Gene, prob, scale float64, rng *rand.Rand) Gene {
	return lunarspotv1.Mutate(gene.(lunarspotv1.Chromosome), prob, scale, rng)
}

func (l *LunarEvolvable) Crossover(p1, p2 Gene, rng *rand.Rand) Gene {
	return lunarspotv1.Crossover(p1.(lunarspotv1.Chromosome), p2.(lunarspotv1.Chromosome), rng)
}

func (l *LunarEvolvable) Fingerprint(gene Gene) uint64 {
	return lunarspotv1.Fingerprint(gene.(lunarspotv1.Chromosome))
}

func (l *LunarEvolvable) DecodeElite(paramPackJSON []byte) Gene {
	if len(paramPackJSON) == 0 {
		return lunarspotv1.DefaultChromosome()
	}
	p, err := lunarspotv1.DecodeParams(paramPackJSON)
	if err != nil {
		return lunarspotv1.DefaultChromosome()
	}
	return p.Chromosome
}

func (l *LunarEvolvable) EncodeResult(gene Gene, spawn SpawnPoint) []byte {
	c, _ := gene.(lunarspotv1.Chromosome)
	sp, _ := spawn.(lunarspotv1.SpawnPoint)
	if (sp == lunarspotv1.SpawnPoint{}) {
		sp = lunarspotv1.DefaultSpawnPoint()
	}
	buf, err := lunarspotv1.EncodeParams(lunarspotv1.Params{
		Chromosome: c,
		SpawnPoint: sp,
	})
	if err != nil {
		return nil
	}
	return buf
}

// Evaluate runs the chromosome against every Crucible window. Both the
// strategy macro engine and the Ghost DCA baseline use the SpawnPoint's
// MonthlyInjectUSDT, so they execute under identical cadence — the only
// alpha source available to GA is the chromosome's discretionary
// (TimeDilation × MacroAccelerator) acceleration plus the micro Sigmoid.
func (l *LunarEvolvable) Evaluate(ctx context.Context, gene Gene, plan EvaluablePlan) EvaluateResult {
	chromo := gene.(lunarspotv1.Chromosome)
	spawn, _ := plan.Spawn.(lunarspotv1.SpawnPoint)
	if (spawn == lunarspotv1.SpawnPoint{}) {
		spawn = lunarspotv1.DefaultSpawnPoint()
	}

	cfg := DefaultSimConfig()
	cfg.InitialCapitalUSDT = spawn.InitialCapitalUSDT
	cfg.MonthlyInjectUSDT = spawn.MonthlyInjectUSDT
	cfg.MinOrderUSDT = plan.MinOrderUSDT
	cfg.LotStepSize = plan.LotStep
	cfg.LotMinQty = plan.LotMin

	params := strategy.Params(lunarspotv1.Params{
		Chromosome: chromo,
		SpawnPoint: spawn,
	})

	fitnessCfg := DefaultFitnessConfig()
	sliceScores := make([]float64, len(plan.Windows))
	sliceMetrics := make([]SliceMetrics, len(plan.Windows))
	maxAcrossWindows := 0.0

	for i, w := range plan.Windows {
		if err := ctx.Err(); err != nil {
			break
		}
		res := SimulateStrategy(l.strat, params, w.Closes, w.Timestamps, w.EvalStartMs, cfg)
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

// Verify is the post-epoch validation pass. Phase 5 reuses the simulator
// without slicing; Monte Carlo etc. is Phase 14+.
func (l *LunarEvolvable) Verify(ctx context.Context, gene Gene, spawn SpawnPoint,
	bars []float64, lotStep, lotMin float64) BacktestMetrics {

	if len(bars) == 0 {
		return BacktestMetrics{}
	}
	chromo := gene.(lunarspotv1.Chromosome)
	sp, _ := spawn.(lunarspotv1.SpawnPoint)
	if (sp == lunarspotv1.SpawnPoint{}) {
		sp = lunarspotv1.DefaultSpawnPoint()
	}

	cfg := DefaultSimConfig()
	cfg.InitialCapitalUSDT = sp.InitialCapitalUSDT
	cfg.MonthlyInjectUSDT = sp.MonthlyInjectUSDT
	cfg.LotStepSize = lotStep
	cfg.LotMinQty = lotMin

	ts := make([]int64, len(bars))
	for i := range ts {
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	res := SimulateStrategy(l.strat, strategy.Params(lunarspotv1.Params{
		Chromosome: chromo,
		SpawnPoint: sp,
	}), bars, ts, ts[0], cfg)

	return BacktestMetrics{
		ROI:         res.ROI,
		MaxDrawdown: res.MaxDrawdown,
		NAVStart:    res.NAVStart,
		NAVEnd:      res.NAVEnd,
	}
}
