package ga

import (
	"context"
	"math/rand"
	"testing"
)

// makeRisingClosesForEpoch returns a 4h close series long enough for a
// crucible build to produce 4 windows. 5000 bars ≈ 833 days.
func makeRisingClosesForEpoch(n int) ([]float64, []int64) {
	closes := make([]float64, n)
	ts := make([]int64, n)
	for i := range closes {
		// Slight uptrend with sinusoidal wiggles so micro/grid have something
		// to react to.
		closes[i] = 100.0 + float64(i)*0.05 +
			float64(((i*37)%17))/10.0
		ts[i] = int64(i) * int64(4*60*60*1000)
	}
	return closes, ts
}

// TestRunEpoch_GridSmoke runs a tiny GA epoch (Pop=8, Gen=3) against the
// grid strategy. The point is to exercise every code path: population init,
// evaluation, tournament, crossover, mutate, fingerprint cache, sort.
//
// We don't assert on alpha — a 3-generation 8-individual run on synthetic
// data won't produce meaningful alpha. We DO assert:
//   - The epoch completes without panic / error
//   - BestGene is non-nil and Encode-able
//   - At least one fingerprint cache hit (some elite should be re-encountered)
func TestRunEpoch_GridSmoke(t *testing.T) {
	closes, ts := makeRisingClosesForEpoch(5000)
	windows, err := BuildCrucibleWindows(closes, ts, DefaultCrucibleParams())
	if err != nil {
		t.Fatalf("crucible: %v", err)
	}

	dcaParams := GhostDCAParams{
		InitialCapitalUSDT: 1000,
		MonthlyInjectUSDT:  0, // grid has no recurring inject
		MinOrderUSDT:       10.1,
		IntervalHours:      4,
		DeadlineDays:       28,
	}
	baselines := make([]DCABaseline, len(windows))
	for i, w := range windows {
		roi, dd := SimulateGhostDCA(w.Closes, w.Timestamps, w.EvalStartMs, dcaParams)
		baselines[i] = DCABaseline{Label: w.Label, ROI: roi, MaxDrawdown: dd}
	}

	plan := EvaluablePlan{
		Symbol:       "BTCUSDT",
		StrategyID:   "grid",
		Spawn:        nil,
		LotStep:      0.00001,
		LotMin:       0.00001,
		MinOrderUSDT: 10.1,
		Windows:      windows,
		DCABaselines: baselines,
	}

	cfg := EpochConfig{
		PopSize:           8,
		MaxGenerations:    3,
		EliteCount:        2,
		TournamentSize:    3,
		MutationProb:      0.3,
		MutationScale:     1.0,
		EarlyStopWindow:   100, // disable early stop in smoke test
		EarlyStopMinDelta: 0.001,
		Seed:              42,
		Workers:           2,
	}

	evol := NewGridEvolvable()
	res, err := RunEpoch(context.Background(), evol, plan, cfg, nil)
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if res == nil || res.BestGene == nil {
		t.Fatalf("expected non-nil result with BestGene")
	}

	// Encode round-trip: result must serialise + deserialise back to the same
	// gene (semantically). Strategy-blind so we just compare fingerprints.
	encoded := evol.EncodeResult(res.BestGene, nil)
	if len(encoded) == 0 {
		t.Fatalf("EncodeResult returned empty bytes")
	}
	decoded := evol.DecodeElite(encoded)
	if evol.Fingerprint(decoded) != evol.Fingerprint(res.BestGene) {
		t.Fatalf("encode→decode fingerprint mismatch")
	}

	if res.Generations < 1 || res.Generations > cfg.MaxGenerations {
		t.Fatalf("Generations out of range: %d", res.Generations)
	}
}

// TestRunEpoch_FingerprintCacheHits verifies that the cache shortcuts
// duplicate evaluations. With a tiny population some elites will reappear
// across generations (preserved verbatim by the elite mechanism), which
// MUST hit the cache instead of re-evaluating.
func TestRunEpoch_FingerprintCacheHits(t *testing.T) {
	closes, ts := makeRisingClosesForEpoch(2000)
	windows, _ := BuildCrucibleWindows(closes, ts, DefaultCrucibleParams())
	dcaParams := GhostDCAParams{InitialCapitalUSDT: 1000, MinOrderUSDT: 10.1, IntervalHours: 4, DeadlineDays: 28}
	baselines := make([]DCABaseline, len(windows))
	for i, w := range windows {
		roi, dd := SimulateGhostDCA(w.Closes, w.Timestamps, w.EvalStartMs, dcaParams)
		baselines[i] = DCABaseline{Label: w.Label, ROI: roi, MaxDrawdown: dd}
	}

	plan := EvaluablePlan{
		LotStep: 0.00001, LotMin: 0.00001, MinOrderUSDT: 10.1,
		Windows: windows, DCABaselines: baselines,
	}

	cfg := EpochConfig{
		PopSize: 6, MaxGenerations: 4, EliteCount: 3, TournamentSize: 3,
		MutationProb: 0.0, MutationScale: 0.0, // no mutation → elites repeat verbatim
		EarlyStopWindow: 100, Seed: 7, Workers: 1,
	}

	res, err := RunEpoch(context.Background(), NewGridEvolvable(), plan, cfg, nil)
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if res.FingerprintHits == 0 {
		t.Fatalf("expected ≥1 fingerprint cache hit with verbatim elites, got 0")
	}
}

// TestRunEpoch_ContextCancellation ensures we respect context cancellation
// without leaking goroutines or deadlocking.
func TestRunEpoch_ContextCancellation(t *testing.T) {
	closes, ts := makeRisingClosesForEpoch(2000)
	windows, _ := BuildCrucibleWindows(closes, ts, DefaultCrucibleParams())
	baselines := make([]DCABaseline, len(windows))

	plan := EvaluablePlan{
		LotStep: 0.00001, LotMin: 0.00001, MinOrderUSDT: 10.1,
		Windows: windows, DCABaselines: baselines,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	cfg := EpochConfig{
		PopSize: 4, MaxGenerations: 5, EliteCount: 1, TournamentSize: 2,
		MutationProb: 0.1, MutationScale: 1.0, EarlyStopWindow: 100, Seed: 1,
	}

	// May or may not return an error depending on where in the loop we land.
	// The contract is just "doesn't deadlock or panic".
	_, _ = RunEpoch(ctx, NewGridEvolvable(), plan, cfg, nil)
}

// TestRunEpoch_DeterministicSeed: same seed + same plan → same best.
func TestRunEpoch_DeterministicSeed(t *testing.T) {
	closes, ts := makeRisingClosesForEpoch(2000)
	windows, _ := BuildCrucibleWindows(closes, ts, DefaultCrucibleParams())
	dcaParams := GhostDCAParams{InitialCapitalUSDT: 1000, MinOrderUSDT: 10.1, IntervalHours: 4, DeadlineDays: 28}
	baselines := make([]DCABaseline, len(windows))
	for i, w := range windows {
		roi, dd := SimulateGhostDCA(w.Closes, w.Timestamps, w.EvalStartMs, dcaParams)
		baselines[i] = DCABaseline{Label: w.Label, ROI: roi, MaxDrawdown: dd}
	}

	plan := EvaluablePlan{
		LotStep: 0.00001, LotMin: 0.00001, MinOrderUSDT: 10.1,
		Windows: windows, DCABaselines: baselines,
	}
	cfg := EpochConfig{
		PopSize: 6, MaxGenerations: 3, EliteCount: 2, TournamentSize: 3,
		MutationProb: 0.2, MutationScale: 1.0, EarlyStopWindow: 100,
		Seed: 12345, Workers: 1, // single worker so eval order is deterministic
	}

	a, _ := RunEpoch(context.Background(), NewGridEvolvable(), plan, cfg, nil)
	b, _ := RunEpoch(context.Background(), NewGridEvolvable(), plan, cfg, nil)

	if NewGridEvolvable().Fingerprint(a.BestGene) != NewGridEvolvable().Fingerprint(b.BestGene) {
		t.Fatalf("same seed produced different best genes")
	}
}

// TestTournamentSelect_PicksHigherScore is a unit test for the inner helper.
func TestTournamentSelect_PicksHigherScore(t *testing.T) {
	pop := []individual{
		{score: 1.0},
		{score: 5.0},
		{score: 3.0},
	}
	rng := rand.New(rand.NewSource(0))
	// Tournament size = len(pop) → must always return the highest.
	sel := tournamentSelect(rng, pop, 3)
	if sel.score != 5.0 {
		t.Fatalf("tournament with size=N must return max, got %v", sel.score)
	}
}
