package ga

import (
	"context"
	"math/rand"
	"runtime"
	"sort"
	"sync"
)

// EpochConfig configures a single GA run.
//
// All defaults match docs/20-進化計算引擎.md §2. Caller can override any
// field; DefaultEpochConfig provides a known-good starting point.
type EpochConfig struct {
	PopSize          int     // total population (incl. elite)
	MaxGenerations   int     // hard upper bound on generations
	EliteCount       int     // top N copied unchanged each generation
	TournamentSize   int
	MutationProb     float64 // initial Bernoulli probability per gene
	MutationScale    float64 // initial Gaussian scale
	MutationProbMax  float64
	MutationScaleMax float64
	MutationRamp     float64 // multiplier applied when stagnating
	EarlyStopWindow  int     // generations of no improvement before ramp/stop
	EarlyStopMinDelta float64
	Seed             int64 // 0 → time-based fallback
	Workers          int   // 0 → runtime.NumCPU()
}

// DefaultEpochConfig returns the canonical configuration (Pop=300, Gen=25, etc.).
func DefaultEpochConfig() EpochConfig {
	return EpochConfig{
		PopSize:          300,
		MaxGenerations:   25,
		EliteCount:       8,
		TournamentSize:   3,
		MutationProb:     0.15,
		MutationScale:    1.0,
		MutationProbMax:  0.55,
		MutationScaleMax: 3.0,
		MutationRamp:     1.25,
		EarlyStopWindow:  5,
		EarlyStopMinDelta: 0.001,
	}
}

// EpochResult is what RunEpoch returns: the best gene found + its score
// and the per-window metrics from its Evaluate call.
type EpochResult struct {
	BestGene        Gene
	BestScore       float64
	BestMetrics     EvaluateResult
	Generations     int  // actual generations run (may be < MaxGenerations on early-stop)
	EarlyStopped    bool
	FingerprintHits int // for diagnostics (cache hit count)
}

// individual is the engine's internal record per population member.
type individual struct {
	gene        Gene
	fingerprint uint64
	score       float64
	metrics     EvaluateResult
	evaluated   bool
}

// RunEpoch executes one full GA epoch. It is purely in-process — basin
// persistence (writing the result row) is the caller's job; this keeps
// engine.go strategy-blind AND DB-agnostic.
//
// elites is a list of pre-discovered seed genes (typically including the
// current champion). Pass nil for cold start; the engine falls back to
// DecodeElite(nil) for the index-0 deterministic seed.
func RunEpoch(ctx context.Context, evol EvolvableStrategy, plan EvaluablePlan,
	cfg EpochConfig, elites []Gene) (*EpochResult, error) {

	cfg = applyConfigDefaults(cfg)
	rng := rand.New(rand.NewSource(cfg.Seed))

	// 1. Population initialization.
	pop := initializePopulation(evol, rng, cfg, elites)

	// 2. Initial evaluation.
	cache := newFingerprintCache()
	evaluatePopulation(ctx, evol, pop, plan, cache, cfg.Workers)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	bestSoFar := bestOf(pop)
	stagnation := 0

	mutProb := cfg.MutationProb
	mutScale := cfg.MutationScale
	earlyStopped := false
	gensRun := 0

	// 3. Main evolution loop.
	for gen := 0; gen < cfg.MaxGenerations; gen++ {
		gensRun = gen + 1
		if err := ctx.Err(); err != nil {
			break
		}

		// Sort current population descending by score.
		sort.Slice(pop, func(i, j int) bool { return pop[i].score > pop[j].score })

		// Build next generation: elites first, then offspring.
		next := make([]individual, 0, cfg.PopSize)
		for k := 0; k < cfg.EliteCount && k < len(pop); k++ {
			next = append(next, pop[k]) // already evaluated, stays scored
		}

		for len(next) < cfg.PopSize {
			p1 := tournamentSelect(rng, pop, cfg.TournamentSize)
			p2 := tournamentSelect(rng, pop, cfg.TournamentSize)
			child := evol.Crossover(p1.gene, p2.gene, rng)
			child = evol.Mutate(child, mutProb, mutScale, rng)
			next = append(next, individual{
				gene:        child,
				fingerprint: evol.Fingerprint(child),
			})
		}

		// Evaluate the freshly-created offspring (skip already-scored elites).
		evaluatePopulation(ctx, evol, next, plan, cache, cfg.Workers)
		pop = next

		// Track best & decide if we ramp / stop.
		thisBest := bestOf(pop)
		improved := thisBest.score-bestSoFar.score > cfg.EarlyStopMinDelta
		if improved {
			bestSoFar = thisBest
			stagnation = 0
		} else {
			stagnation++
		}

		if stagnation >= cfg.EarlyStopWindow {
			// Ramp mutation if any room left, else stop.
			ramped := false
			if mutProb < cfg.MutationProbMax {
				mutProb *= cfg.MutationRamp
				if mutProb > cfg.MutationProbMax {
					mutProb = cfg.MutationProbMax
				}
				ramped = true
			}
			if mutScale < cfg.MutationScaleMax {
				mutScale *= cfg.MutationRamp
				if mutScale > cfg.MutationScaleMax {
					mutScale = cfg.MutationScaleMax
				}
				ramped = true
			}
			if !ramped {
				earlyStopped = true
				break
			}
			stagnation = 0
		}
	}

	return &EpochResult{
		BestGene:        bestSoFar.gene,
		BestScore:       bestSoFar.score,
		BestMetrics:     bestSoFar.metrics,
		Generations:     gensRun,
		EarlyStopped:    earlyStopped,
		FingerprintHits: cache.hits,
	}, nil
}

// applyConfigDefaults fills zero-valued fields with DefaultEpochConfig values.
func applyConfigDefaults(cfg EpochConfig) EpochConfig {
	d := DefaultEpochConfig()
	if cfg.PopSize <= 0 {
		cfg.PopSize = d.PopSize
	}
	if cfg.MaxGenerations <= 0 {
		cfg.MaxGenerations = d.MaxGenerations
	}
	if cfg.EliteCount <= 0 {
		cfg.EliteCount = d.EliteCount
	}
	if cfg.TournamentSize <= 0 {
		cfg.TournamentSize = d.TournamentSize
	}
	if cfg.MutationProb <= 0 {
		cfg.MutationProb = d.MutationProb
	}
	if cfg.MutationScale <= 0 {
		cfg.MutationScale = d.MutationScale
	}
	if cfg.MutationProbMax <= 0 {
		cfg.MutationProbMax = d.MutationProbMax
	}
	if cfg.MutationScaleMax <= 0 {
		cfg.MutationScaleMax = d.MutationScaleMax
	}
	if cfg.MutationRamp <= 0 {
		cfg.MutationRamp = d.MutationRamp
	}
	if cfg.EarlyStopWindow <= 0 {
		cfg.EarlyStopWindow = d.EarlyStopWindow
	}
	if cfg.EarlyStopMinDelta == 0 {
		cfg.EarlyStopMinDelta = d.EarlyStopMinDelta
	}
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.NumCPU()
	}
	if cfg.PopSize < cfg.Workers {
		cfg.Workers = cfg.PopSize
	}
	return cfg
}

// initializePopulation builds the seed generation.
//
// Layout matches docs §2.1:
//   - index 0 = the deterministic default seed (or the first user-supplied elite)
//   - 10% pure copies of remaining elites (round-robin)
//   - 40% reinforced-mutation copies of elites (prob=0.15, scale=1.5)
//   - 50% fully random Sample()
//
// When no elites are supplied, only index 0 is the default seed and the rest
// are pure Sample().
func initializePopulation(evol EvolvableStrategy, rng *rand.Rand,
	cfg EpochConfig, elites []Gene) []individual {

	pop := make([]individual, 0, cfg.PopSize)

	// Index 0: default seed, or first elite if provided.
	var seed Gene
	if len(elites) > 0 {
		seed = elites[0]
	} else {
		seed = evol.DecodeElite(nil)
	}
	pop = append(pop, individual{gene: seed, fingerprint: evol.Fingerprint(seed)})

	if len(elites) == 0 {
		// Cold start: index 1+ all random.
		for len(pop) < cfg.PopSize {
			g := evol.Sample(rng)
			pop = append(pop, individual{gene: g, fingerprint: evol.Fingerprint(g)})
		}
		return pop
	}

	// Warm start: 10/40/50 split among the remaining slots.
	remaining := cfg.PopSize - 1
	pureCount := remaining * 10 / 100
	mutCount := remaining * 40 / 100
	randCount := remaining - pureCount - mutCount // absorbs rounding

	for i := 0; i < pureCount; i++ {
		g := elites[i%len(elites)]
		pop = append(pop, individual{gene: g, fingerprint: evol.Fingerprint(g)})
	}
	for i := 0; i < mutCount; i++ {
		g := evol.Mutate(elites[i%len(elites)], 0.15, 1.5, rng)
		pop = append(pop, individual{gene: g, fingerprint: evol.Fingerprint(g)})
	}
	for i := 0; i < randCount; i++ {
		g := evol.Sample(rng)
		pop = append(pop, individual{gene: g, fingerprint: evol.Fingerprint(g)})
	}
	return pop
}

// fingerprintCache memoises Evaluate results within one epoch. Two genes
// with the same Fingerprint (semantic equality at 1e-6 precision) share
// one Evaluate call.
type fingerprintCache struct {
	mu      sync.Mutex
	results map[uint64]EvaluateResult
	hits    int
}

func newFingerprintCache() *fingerprintCache {
	return &fingerprintCache{results: make(map[uint64]EvaluateResult)}
}

func (c *fingerprintCache) get(fp uint64) (EvaluateResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.results[fp]
	if ok {
		c.hits++
	}
	return r, ok
}

func (c *fingerprintCache) put(fp uint64, r EvaluateResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[fp] = r
}

// evaluatePopulation fills in score + metrics for every NOT-yet-evaluated
// individual in pop. Uses a worker pool of cfg.Workers goroutines.
func evaluatePopulation(ctx context.Context, evol EvolvableStrategy,
	pop []individual, plan EvaluablePlan, cache *fingerprintCache, workers int) {

	// Channel of indices to evaluate.
	jobs := make(chan int, len(pop))
	for i, ind := range pop {
		if !ind.evaluated {
			jobs <- i
		}
	}
	close(jobs)

	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					return
				}
				ind := &pop[i]
				if r, ok := cache.get(ind.fingerprint); ok {
					ind.score = r.ScoreTotal
					ind.metrics = r
					ind.evaluated = true
					continue
				}
				r := evol.Evaluate(ctx, ind.gene, plan)
				cache.put(ind.fingerprint, r)
				ind.score = r.ScoreTotal
				ind.metrics = r
				ind.evaluated = true
			}
		}()
	}
	wg.Wait()
}

// tournamentSelect picks `size` random distinct individuals and returns the
// one with the highest score. We pass by value (individual is small) — no
// pointer aliasing surprises.
func tournamentSelect(rng *rand.Rand, pop []individual, size int) individual {
	if size > len(pop) {
		size = len(pop)
	}
	best := pop[rng.Intn(len(pop))]
	for i := 1; i < size; i++ {
		c := pop[rng.Intn(len(pop))]
		if c.score > best.score {
			best = c
		}
	}
	return best
}

// bestOf returns the highest-scoring individual in pop.
func bestOf(pop []individual) individual {
	best := pop[0]
	for _, ind := range pop[1:] {
		if ind.score > best.score {
			best = ind
		}
	}
	return best
}
