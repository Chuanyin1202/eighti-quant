package grid

import (
	"math/rand"
	"testing"
)

func TestSample_AllInBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		p := Sample(rng)
		if p.LowerPriceRel < 0.5 || p.LowerPriceRel > 0.95 {
			t.Fatalf("LowerPriceRel out of bounds: %v", p.LowerPriceRel)
		}
		if p.UpperPriceRel < 1.05 || p.UpperPriceRel > 1.5 {
			t.Fatalf("UpperPriceRel out of bounds: %v", p.UpperPriceRel)
		}
		if p.GridCount < 5 || p.GridCount > 30 {
			t.Fatalf("GridCount out of bounds: %v", p.GridCount)
		}
		if p.OrderUSDTRatio < 0.005 || p.OrderUSDTRatio > 0.05 {
			t.Fatalf("OrderUSDTRatio out of bounds: %v", p.OrderUSDTRatio)
		}
		if p.RebalanceCooldownBars < 1 || p.RebalanceCooldownBars > 24 {
			t.Fatalf("RebalanceCooldownBars out of bounds: %v", p.RebalanceCooldownBars)
		}
		if p.UpperPriceRel-p.LowerPriceRel < 0.1-1e-9 {
			t.Fatalf("structural constraint violated: upper-lower=%v", p.UpperPriceRel-p.LowerPriceRel)
		}
	}
}

func TestMutate_StaysInBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	parent := Sample(rng)
	for i := 0; i < 1000; i++ {
		child := Mutate(parent, 1.0, 5.0, rng) // very aggressive mutation
		if child.GridCount < 5 || child.GridCount > 30 {
			t.Fatalf("GridCount escaped: %v", child.GridCount)
		}
		if child.UpperPriceRel-child.LowerPriceRel < 0.1-1e-9 {
			t.Fatalf("structural constraint violated after mutate")
		}
		parent = child
	}
}

func TestCrossover_StaysInBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 1000; i++ {
		c := Crossover(Sample(rng), Sample(rng), rng)
		if c.LowerPriceRel < 0.5 || c.LowerPriceRel > 0.95 {
			t.Fatalf("crossover lower out of bounds: %v", c.LowerPriceRel)
		}
		if c.UpperPriceRel-c.LowerPriceRel < 0.1-1e-9 {
			t.Fatalf("crossover structural violated")
		}
	}
}

func TestFingerprint_StableUnderEpsilon(t *testing.T) {
	p1 := Params{LowerPriceRel: 0.7000001, UpperPriceRel: 1.3000001, GridCount: 10,
		OrderUSDTRatio: 0.020000001, RebalanceCooldownBars: 6}
	p2 := Params{LowerPriceRel: 0.7, UpperPriceRel: 1.3, GridCount: 10,
		OrderUSDTRatio: 0.02, RebalanceCooldownBars: 6}
	if Fingerprint(p1) != Fingerprint(p2) {
		t.Fatalf("epsilon-equivalent params produced different fingerprints")
	}
}

func TestFingerprint_DiffersWhenFieldChanges(t *testing.T) {
	p1 := Params{LowerPriceRel: 0.7, UpperPriceRel: 1.3, GridCount: 10,
		OrderUSDTRatio: 0.02, RebalanceCooldownBars: 6}
	p2 := p1
	p2.GridCount = 11
	if Fingerprint(p1) == Fingerprint(p2) {
		t.Fatalf("GridCount change must change fingerprint")
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	orig := Sample(rng)
	buf, err := EncodeParams(orig)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeParams(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != orig {
		t.Fatalf("round-trip mismatch:\norig=%+v\n got=%+v", orig, got)
	}
}

func TestDecode_EmptyReturnsDefaults(t *testing.T) {
	got, err := DecodeParams(nil)
	if err != nil {
		t.Fatalf("decode nil: %v", err)
	}
	want := (&Strategy{}).DefaultParams().(Params)
	if got != want {
		t.Fatalf("empty decode should equal defaults:\n got=%+v\nwant=%+v", got, want)
	}
}
