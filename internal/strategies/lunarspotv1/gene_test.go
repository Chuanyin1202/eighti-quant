package lunarspotv1

import (
	"math"
	"math/rand"
	"testing"
)

func TestSample_AllInBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		c := Sample(rng)
		assertInBounds(t, c)
	}
}

func TestMutate_StaysInBoundsAtExtremeScale(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	parent := Sample(rng)
	for i := 0; i < 1000; i++ {
		child := Mutate(parent, 1.0, 5.0, rng)
		assertInBounds(t, child)
		parent = child
	}
}

func TestCrossover_StaysInBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 1000; i++ {
		c := Crossover(Sample(rng), Sample(rng), rng)
		assertInBounds(t, c)
	}
}

func TestFingerprint_StableUnderEpsilon(t *testing.T) {
	a := DefaultChromosome()
	b := a
	b.Beta += 0.0000001
	b.Gamma -= 0.00000001
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatalf("epsilon-equivalent chromosomes produced different fingerprints")
	}
}

func TestFingerprint_DistinguishesDifferentChromosomes(t *testing.T) {
	a := DefaultChromosome()
	b := a
	b.MacroDeadlineDays = a.MacroDeadlineDays + 1
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatalf("integer field change must change fingerprint")
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	orig := Params{
		Chromosome: Sample(rng),
		SpawnPoint: SpawnPoint{
			MonthlyInjectUSDT: 250, InitialCapitalUSDT: 5000,
			ColdSealedPct: 0.05, GlobalStopLossPct: 0.6,
		},
	}
	buf, err := EncodeParams(orig)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeParams(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The Clamp applied during Encode should be idempotent on re-clamp.
	wantClamped := Clamp(orig)
	if got.Chromosome != wantClamped.Chromosome {
		t.Fatalf("chromosome round-trip mismatch:\n orig=%+v\n got=%+v", wantClamped.Chromosome, got.Chromosome)
	}
	if got.SpawnPoint != orig.SpawnPoint {
		t.Fatalf("spawn round-trip mismatch:\n orig=%+v\n got=%+v", orig.SpawnPoint, got.SpawnPoint)
	}
}

func TestDecode_EmptyReturnsDefaults(t *testing.T) {
	got, err := DecodeParams(nil)
	if err != nil {
		t.Fatalf("decode nil: %v", err)
	}
	wantC := DefaultChromosome()
	if got.Chromosome != wantC {
		t.Fatalf("empty decode chromosome != default:\n got=%+v\nwant=%+v", got.Chromosome, wantC)
	}
}

// assertInBounds verifies every chromosome field is within the spec's hard
// bounds AND the structural constraint (sum of |W| ≥ 0.1).
func assertInBounds(t *testing.T, c Chromosome) {
	t.Helper()
	check := func(name string, v, lo, hi float64) {
		if v < lo-1e-9 || v > hi+1e-9 {
			t.Fatalf("%s out of bounds: got %v, want [%v, %v]", name, v, lo, hi)
		}
	}
	check("Beta", c.Beta, 0.1, 5.0)
	check("Gamma", c.Gamma, 0, 2.0)
	check("SigmaFloor", c.SigmaFloor, 0.001, 0.05)
	check("SignalW_P", c.SignalW_P, -2.0, 2.0)
	check("SignalW_V", c.SignalW_V, -2.0, 2.0)
	check("SignalW_A", c.SignalW_A, -2.0, 2.0)
	check("WedgeDeltaThr", c.WedgeDeltaThr, 0.01, 0.1)
	check("MacroAccelerator", c.MacroAccelerator, 1.0, 3.0)
	check("TargetMicroWeightFloor", c.TargetMicroWeightFloor, 0.05, 0.49)

	if c.EMABaseBars < 50 || c.EMABaseBars > 600 {
		t.Fatalf("EMABaseBars out of bounds: %v", c.EMABaseBars)
	}
	if c.SigmaWindow < 10 || c.SigmaWindow > 100 {
		t.Fatalf("SigmaWindow out of bounds: %v", c.SigmaWindow)
	}
	if c.VelocityLookback < 1 || c.VelocityLookback > 10 {
		t.Fatalf("VelocityLookback out of bounds: %v", c.VelocityLookback)
	}
	if c.MacroDeadlineDays < 25 || c.MacroDeadlineDays > 30 {
		t.Fatalf("MacroDeadlineDays out of bounds: %v", c.MacroDeadlineDays)
	}
	if c.DeadAgingMonths < 3 || c.DeadAgingMonths > 24 {
		t.Fatalf("DeadAgingMonths out of bounds: %v", c.DeadAgingMonths)
	}

	if math.Abs(c.SignalW_P)+math.Abs(c.SignalW_V)+math.Abs(c.SignalW_A) < 0.1-1e-9 {
		t.Fatalf("structural constraint violated: sum |W| < 0.1")
	}
}
