package grid

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math"
	"math/rand"
)

// Gene operations for GA. These are pure functions over Params; they don't
// import the framework's `ga` package, so they break no dependency cycle.
//
// The framework's GA engine wraps these functions through an adapter
// (`internal/saas/ga/grid_evolvable.go`, written in Phase 5) so the engine
// itself stays strategy-agnostic.

// Sample draws a random Params from the legal space and Clamps it.
func Sample(rng *rand.Rand) Params {
	p := Params{
		LowerPriceRel:         0.5 + rng.Float64()*0.45,   // [0.5, 0.95]
		UpperPriceRel:         1.05 + rng.Float64()*0.45,  // [1.05, 1.5]
		GridCount:             5 + rng.Intn(26),           // [5, 30]
		OrderUSDTRatio:        0.005 + rng.Float64()*0.045, // [0.005, 0.05]
		RebalanceCooldownBars: 1 + rng.Intn(24),           // [1, 24]
	}
	return Clamp(p)
}

// Mutate applies independent Bernoulli(prob) Gaussian mutation to each
// dimension. The step size for each field is roughly the sample-space range
// divided by 10, which keeps mutations meaningful but not catastrophic.
//
// Always returns a Clamp'd Params — no out-of-range gene leaks downstream.
func Mutate(p Params, prob, scale float64, rng *rand.Rand) Params {
	if rng.Float64() < prob {
		p.LowerPriceRel += rng.NormFloat64() * 0.05 * scale
	}
	if rng.Float64() < prob {
		p.UpperPriceRel += rng.NormFloat64() * 0.05 * scale
	}
	if rng.Float64() < prob {
		p.GridCount += int(math.Round(rng.NormFloat64() * 1.0 * scale))
	}
	if rng.Float64() < prob {
		p.OrderUSDTRatio += rng.NormFloat64() * 0.005 * scale
	}
	if rng.Float64() < prob {
		p.RebalanceCooldownBars += int(math.Round(rng.NormFloat64() * 2.0 * scale))
	}
	return Clamp(p)
}

// Crossover performs uniform crossover: each field independently picks
// from p1 or p2 with 0.5 probability. Result is Clamp'd.
func Crossover(p1, p2 Params, rng *rand.Rand) Params {
	pick := func(a, b float64) float64 {
		if rng.Float64() < 0.5 {
			return a
		}
		return b
	}
	pickInt := func(a, b int) int {
		if rng.Float64() < 0.5 {
			return a
		}
		return b
	}
	c := Params{
		LowerPriceRel:         pick(p1.LowerPriceRel, p2.LowerPriceRel),
		UpperPriceRel:         pick(p1.UpperPriceRel, p2.UpperPriceRel),
		GridCount:             pickInt(p1.GridCount, p2.GridCount),
		OrderUSDTRatio:        pick(p1.OrderUSDTRatio, p2.OrderUSDTRatio),
		RebalanceCooldownBars: pickInt(p1.RebalanceCooldownBars, p2.RebalanceCooldownBars),
	}
	return Clamp(c)
}

// Fingerprint hashes a Params at 1e-6 precision so semantically-equivalent
// chromosomes (within float drift) collide in the GA fingerprint cache.
//
// Encoding choice: round each float field to int64 nanoscale, write all
// fields in fixed order to FNV-1a-64. Integer fields are hashed as int64
// directly (Clamp guarantees they're already canonical).
func Fingerprint(p Params) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	write := func(x int64) {
		binary.LittleEndian.PutUint64(buf[:], uint64(x))
		_, _ = h.Write(buf[:])
	}
	write(int64(math.Round(p.LowerPriceRel * 1e6)))
	write(int64(math.Round(p.UpperPriceRel * 1e6)))
	write(int64(p.GridCount))
	write(int64(math.Round(p.OrderUSDTRatio * 1e6)))
	write(int64(p.RebalanceCooldownBars))
	return h.Sum64()
}

// EncodeParams serializes a Params for the GA basin (gene_records.param_pack_json).
// Always Clamp before encoding so what we persist is what would have run.
func EncodeParams(p Params) ([]byte, error) {
	return json.Marshal(Clamp(p))
}

// DecodeParams parses a Params back from a JSON ParamPack.
// Empty / nil bytes return DefaultParams so cold-start fallback is implicit.
func DecodeParams(raw []byte) (Params, error) {
	if len(raw) == 0 {
		return (&Strategy{}).DefaultParams().(Params), nil
	}
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return Params{}, err
	}
	return Clamp(p), nil
}
