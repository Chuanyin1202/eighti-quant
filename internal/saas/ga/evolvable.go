// Package ga is the strategy-blind genetic algorithm engine.
//
// Iron rule (CLAUDE.md §2.9): nothing in this package may import
// internal/strategies/. Strategies plug in via the EvolvableStrategy
// interface, which is implemented by per-strategy adapters (e.g.
// internal/saas/ga/grid_evolvable.go) that DO import the strategy package.
//
// File layout:
//   evolvable.go - interface + plan + result types (this file)
//   crucible.go  - 4-window construction
//   ghost_dca.go - per-tick + carry baseline simulator
//   fitness.go   - Alpha / drawdown penalty / window-local fatal scoring
//   simulator.go - shared backtest harness for Strategy.Step()
//   engine.go    - GA main loop (population, tournament, crossover, mutate, early-stop)
//   <strategy>_evolvable.go - per-strategy adapter (one per evolvable strategy)
package ga

import (
	"context"
	"math/rand"
)

// Gene is the opaque chromosome carrier. The GA engine never inspects
// internal fields; it only passes Gene values back through 8-verb operations.
type Gene = any

// SpawnPoint is the per-instance non-evolving config carrier (also opaque
// to the engine). Each strategy defines its own concrete SpawnPoint type;
// the framework just round-trips it.
type SpawnPoint = any

// EvolvableStrategy is the contract a strategy adapter must satisfy for the
// engine to evolve it. It is a SUPER-set of strategy.Strategy in spirit,
// but lives in this package to break the strategy → ga circular dependency.
//
// Implementations live in internal/saas/ga/<strategy>_evolvable.go and
// import the strategy package directly.
type EvolvableStrategy interface {
	// StrategyID returns the unique identifier (matches strategy.Manifest().ID).
	// The engine uses this to key basin rows and disambiguate logs.
	StrategyID() string

	// Sample draws a uniformly-random Gene from the legal hyperbox. Result
	// MUST already be Clamp'd; the engine treats it as ready-to-use.
	Sample(rng *rand.Rand) Gene

	// Mutate applies independent Bernoulli(prob) Gaussian mutation to each
	// dimension. scale tunes the magnitude (1.0 = nominal, 3.0 = ramped up).
	// Result MUST be Clamp'd.
	Mutate(g Gene, prob, scale float64, rng *rand.Rand) Gene

	// Crossover performs uniform crossover between two parents.
	// Result MUST be Clamp'd.
	Crossover(p1, p2 Gene, rng *rand.Rand) Gene

	// Fingerprint quantises the Gene at 1e-6 precision and hashes deterministically.
	// Two genes that are semantically identical (within float drift) MUST collide.
	Fingerprint(g Gene) uint64

	// Evaluate runs the gene against all Crucible windows and returns the
	// composite score plus per-window detail.
	Evaluate(ctx context.Context, g Gene, plan EvaluablePlan) EvaluateResult

	// DecodeElite parses a basin ParamPack JSON back into a Gene. Empty
	// input MUST return the deterministic default seed (see strategy spec).
	DecodeElite(paramPackJSON []byte) Gene

	// EncodeResult serialises a (Gene, SpawnPoint) pair as ParamPack JSON
	// for basin persistence. EncodeResult always Clamps before serialising.
	EncodeResult(g Gene, spawn SpawnPoint) []byte

	// Verify is the post-epoch validation pass — full-history backtest with
	// extra robustness checks (Monte-Carlo etc. are PLANNED, not yet wired).
	// For Phase 5 this can return whatever the simulator produces.
	Verify(ctx context.Context, g Gene, spawn SpawnPoint, bars []float64,
		lotStep, lotMin float64) BacktestMetrics
}

// CrucibleWindow is one of the four time-slice evaluation windows.
//
// Closes is the CLOSE-only price series for this window, INCLUDING warm-up.
// EvalStartMs is the timestamp of the first bar that counts towards scoring;
// any bar with ts < EvalStartMs is warm-up only and MUST NOT influence ROI
// or drawdown calculations.
type CrucibleWindow struct {
	Label       string    // "6m" / "2y" / "5y" / "10y"
	Weight      float64   // 0.10 / 0.20 / 0.30 / 0.40
	Closes      []float64 // ascending; warmup prefix + eval region
	Timestamps  []int64   // same length as Closes
	EvalStartMs int64     // first ms where evaluation starts
}

// DCABaseline is the precomputed Ghost DCA result for one window. Cached at
// EvaluablePlan construction time so the per-genome Evaluate doesn't re-run
// the baseline simulation on every chromosome.
type DCABaseline struct {
	Label       string
	ROI         float64
	MaxDrawdown float64
}

// EvaluablePlan is the read-only context handed to every Evaluate call
// during one GA epoch. Strategies use Spawn / LotStep / LotMin to configure
// their simulator state; Windows + DCABaselines drive the per-window scoring.
type EvaluablePlan struct {
	Symbol        string
	StrategyID    string
	Spawn         SpawnPoint
	LotStep       float64
	LotMin        float64
	MinOrderUSDT  float64
	Windows       []CrucibleWindow
	DCABaselines  []DCABaseline // index-aligned with Windows
}

// SliceMetrics is the per-window backtest output a strategy's Evaluate fills in.
type SliceMetrics struct {
	Label       string
	ROI         float64
	MaxDrawdown float64
}

// EvaluateResult is what Evaluate returns: the aggregated total + per-window
// detail (for diagnostics + UI).
type EvaluateResult struct {
	ScoreTotal  float64
	Windows     []SliceMetrics
	MaxDrawdown float64 // max across windows, for basin persistence
}

// BacktestMetrics is the verify-pass full-history output. Phase 5 keeps it
// intentionally minimal — Monte Carlo etc. land in Phase 14+.
type BacktestMetrics struct {
	ROI         float64
	MaxDrawdown float64
	NAVStart    float64
	NAVEnd      float64
}
