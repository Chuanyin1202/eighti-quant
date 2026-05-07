package lunarspotv1

import (
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math"
	"math/rand"
)

// Gene operations for GA.
//
// All operations work on the Chromosome only — SpawnPoint is never mutated
// by the GA. The framework's adapter (internal/saas/ga/lunar_spot_v1_evolvable.go,
// Phase 5) wraps these with the EvolvableStrategy interface and threads
// SpawnPoint through unchanged.

// Sample draws a uniformly-random Chromosome from the legal hyperbox and
// runs Clamp to enforce structural constraints.
func Sample(rng *rand.Rand) Chromosome {
	c := Chromosome{
		Beta:                   0.1 + rng.Float64()*4.9,    // [0.1, 5.0]
		Gamma:                  rng.Float64() * 2.0,        // [0, 2]
		SigmaFloor:             0.001 + rng.Float64()*0.049, // [0.001, 0.05]
		SignalW_P:              -2.0 + rng.Float64()*4.0,
		SignalW_V:              -2.0 + rng.Float64()*4.0,
		SignalW_A:              -2.0 + rng.Float64()*4.0,
		EMABaseBars:            50 + rng.Intn(551),         // [50, 600]
		SigmaWindow:            10 + rng.Intn(91),          // [10, 100]
		VelocityLookback:       1 + rng.Intn(10),           // [1, 10]
		WedgeDeltaThr:          0.01 + rng.Float64()*0.09,  // [0.01, 0.1]
		MacroAccelerator:       1.0 + rng.Float64()*2.0,    // [1.0, 3.0]
		MacroDeadlineDays:      25 + rng.Intn(6),           // [25, 30]
		DeadAgingMonths:        3 + rng.Intn(22),           // [3, 24]
		TargetMicroWeightFloor: 0.05 + rng.Float64()*0.44,  // [0.05, 0.49]
	}
	return Clamp(Params{Chromosome: c}).Chromosome
}

// Mutate applies independent Bernoulli(prob) Gaussian mutation to each
// chromosome dimension. Step sizes per field are calibrated to roughly
// 1/10 of the legal range — meaningful but not catastrophic.
func Mutate(c Chromosome, prob, scale float64, rng *rand.Rand) Chromosome {
	if rng.Float64() < prob {
		c.Beta += rng.NormFloat64() * 0.1 * scale
	}
	if rng.Float64() < prob {
		c.Gamma += rng.NormFloat64() * 0.05 * scale
	}
	if rng.Float64() < prob {
		c.SigmaFloor += rng.NormFloat64() * 0.001 * scale
	}
	if rng.Float64() < prob {
		c.SignalW_P += rng.NormFloat64() * 0.1 * scale
	}
	if rng.Float64() < prob {
		c.SignalW_V += rng.NormFloat64() * 0.1 * scale
	}
	if rng.Float64() < prob {
		c.SignalW_A += rng.NormFloat64() * 0.1 * scale
	}
	if rng.Float64() < prob {
		c.EMABaseBars += int(math.Round(rng.NormFloat64() * 50 * scale))
	}
	if rng.Float64() < prob {
		c.SigmaWindow += int(math.Round(rng.NormFloat64() * 10 * scale))
	}
	if rng.Float64() < prob {
		c.VelocityLookback += int(math.Round(rng.NormFloat64() * 1 * scale))
	}
	if rng.Float64() < prob {
		c.WedgeDeltaThr += rng.NormFloat64() * 0.005 * scale
	}
	if rng.Float64() < prob {
		c.MacroAccelerator += rng.NormFloat64() * 0.1 * scale
	}
	if rng.Float64() < prob {
		c.MacroDeadlineDays += int(math.Round(rng.NormFloat64() * 1 * scale))
	}
	if rng.Float64() < prob {
		c.DeadAgingMonths += int(math.Round(rng.NormFloat64() * 1 * scale))
	}
	if rng.Float64() < prob {
		c.TargetMicroWeightFloor += rng.NormFloat64() * 0.01 * scale
	}
	return Clamp(Params{Chromosome: c}).Chromosome
}

// Crossover performs uniform crossover: each field independently picks
// from p1 or p2 with 0.5 probability. Result is Clamp'd.
func Crossover(p1, p2 Chromosome, rng *rand.Rand) Chromosome {
	pickF := func(a, b float64) float64 {
		if rng.Float64() < 0.5 {
			return a
		}
		return b
	}
	pickI := func(a, b int) int {
		if rng.Float64() < 0.5 {
			return a
		}
		return b
	}
	c := Chromosome{
		Beta:                   pickF(p1.Beta, p2.Beta),
		Gamma:                  pickF(p1.Gamma, p2.Gamma),
		SigmaFloor:             pickF(p1.SigmaFloor, p2.SigmaFloor),
		SignalW_P:              pickF(p1.SignalW_P, p2.SignalW_P),
		SignalW_V:              pickF(p1.SignalW_V, p2.SignalW_V),
		SignalW_A:              pickF(p1.SignalW_A, p2.SignalW_A),
		EMABaseBars:            pickI(p1.EMABaseBars, p2.EMABaseBars),
		SigmaWindow:            pickI(p1.SigmaWindow, p2.SigmaWindow),
		VelocityLookback:       pickI(p1.VelocityLookback, p2.VelocityLookback),
		WedgeDeltaThr:          pickF(p1.WedgeDeltaThr, p2.WedgeDeltaThr),
		MacroAccelerator:       pickF(p1.MacroAccelerator, p2.MacroAccelerator),
		MacroDeadlineDays:      pickI(p1.MacroDeadlineDays, p2.MacroDeadlineDays),
		DeadAgingMonths:        pickI(p1.DeadAgingMonths, p2.DeadAgingMonths),
		TargetMicroWeightFloor: pickF(p1.TargetMicroWeightFloor, p2.TargetMicroWeightFloor),
	}
	return Clamp(Params{Chromosome: c}).Chromosome
}

// Fingerprint hashes a Chromosome at 1e-6 precision so semantically-
// equivalent chromosomes (within float drift) collide in the GA fingerprint
// cache.
func Fingerprint(c Chromosome) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	w := func(x int64) {
		binary.LittleEndian.PutUint64(buf[:], uint64(x))
		_, _ = h.Write(buf[:])
	}
	q := func(x float64) int64 { return int64(math.Round(x * 1e6)) }

	w(q(c.Beta))
	w(q(c.Gamma))
	w(q(c.SigmaFloor))
	w(q(c.SignalW_P))
	w(q(c.SignalW_V))
	w(q(c.SignalW_A))
	w(int64(c.EMABaseBars))
	w(int64(c.SigmaWindow))
	w(int64(c.VelocityLookback))
	w(q(c.WedgeDeltaThr))
	w(q(c.MacroAccelerator))
	w(int64(c.MacroDeadlineDays))
	w(int64(c.DeadAgingMonths))
	w(q(c.TargetMicroWeightFloor))
	return h.Sum64()
}

// EncodeParams serializes Params (chromosome + spawn) for the basin.
func EncodeParams(p Params) ([]byte, error) {
	return json.Marshal(Clamp(p))
}

// DecodeParams parses a Params back from a JSON ParamPack. Empty input
// returns DefaultChromosome + DefaultSpawnPoint, the deterministic seed.
func DecodeParams(raw []byte) (Params, error) {
	if len(raw) == 0 {
		return Clamp(Params{
			Chromosome: DefaultChromosome(),
			SpawnPoint: DefaultSpawnPoint(),
		}), nil
	}
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return Params{}, err
	}
	return Clamp(p), nil
}
