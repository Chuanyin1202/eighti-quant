package quant

import (
	"math"
	"testing"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

func nearlyEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func makeLots() []strategy.LotEntry {
	// 3 DEAD, 2 FLOAT, 1 COLD_SEALED, with varying BuyTimeMs.
	return []strategy.LotEntry{
		{ID: 1, Type: "DEAD", Qty: 1.0, BuyTimeMs: 1000},
		{ID: 2, Type: "DEAD", Qty: 2.0, BuyTimeMs: 3000, IsColdSealed: false},
		{ID: 3, Type: "DEAD", Qty: 0.5, BuyTimeMs: 2000, IsColdSealed: true}, // sealed
		{ID: 4, Type: "FLOAT", Qty: 0.3, BuyTimeMs: 4000},
		{ID: 5, Type: "FLOAT", Qty: 0.7, BuyTimeMs: 5000},
		{ID: 6, Type: "COLD_SEALED", Qty: 0.2, BuyTimeMs: 100, IsColdSealed: true},
	}
}

func TestFilterLots_TypeAndNotColdSealed(t *testing.T) {
	lots := makeLots()
	got := FilterLots(lots, And(IsType("DEAD"), NotColdSealed()))
	if len(got) != 2 {
		t.Fatalf("want 2 dead-non-sealed, got %d", len(got))
	}
	for _, l := range got {
		if l.Type != "DEAD" || l.IsColdSealed {
			t.Fatalf("filter failed: %+v", l)
		}
	}
}

func TestFilterLots_AgedAtLeast(t *testing.T) {
	lots := makeLots()
	now := int64(5500)
	got := FilterLots(lots, And(IsType("DEAD"), AgedAtLeast(now, 2000)))
	// Aged DEAD: id=1 (now-1000=4500≥2000) and id=2 (now-3000=2500≥2000) and id=3 (sealed but type=DEAD, age=3500≥2000)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}

func TestSortByBuyTimeMsAsc_StableFIFO(t *testing.T) {
	lots := []strategy.LotEntry{
		{ID: 10, BuyTimeMs: 5},
		{ID: 11, BuyTimeMs: 2},
		{ID: 12, BuyTimeMs: 9},
	}
	got := SortByBuyTimeMsAsc(lots)
	if got[0].ID != 11 || got[1].ID != 10 || got[2].ID != 12 {
		t.Fatalf("unexpected order: %+v", got)
	}
	// Original slice must be untouched.
	if lots[0].ID != 10 {
		t.Fatalf("original slice was mutated")
	}
}

func TestSumQty(t *testing.T) {
	got := SumQty([]strategy.LotEntry{{Qty: 1.5}, {Qty: 2.5}, {Qty: 0.0}})
	if !nearlyEqual(got, 4.0) {
		t.Fatalf("SumQty: want 4, got %v", got)
	}
}

func TestAccumulateSlices_PartialOnLastLot(t *testing.T) {
	lots := []strategy.LotEntry{
		{ID: 1, Qty: 0.3, BuyTimeMs: 1},
		{ID: 2, Qty: 0.5, BuyTimeMs: 2},
		{ID: 3, Qty: 0.4, BuyTimeMs: 3},
	}
	slices, filled := AccumulateSlices(lots, 0.6)
	// Expect ID=1 take 0.3, ID=2 take 0.3 (partial), ID=3 not touched.
	if len(slices) != 2 {
		t.Fatalf("want 2 slices, got %d (%+v)", len(slices), slices)
	}
	if slices[0].LotID != 1 || !nearlyEqual(slices[0].Qty, 0.3) {
		t.Fatalf("slice 0: %+v", slices[0])
	}
	if slices[1].LotID != 2 || !nearlyEqual(slices[1].Qty, 0.3) {
		t.Fatalf("slice 1: %+v", slices[1])
	}
	if !nearlyEqual(filled, 0.6) {
		t.Fatalf("filled: want 0.6, got %v", filled)
	}
}

func TestAccumulateSlices_ExactWholeLot(t *testing.T) {
	lots := []strategy.LotEntry{
		{ID: 1, Qty: 0.5},
		{ID: 2, Qty: 0.5},
	}
	slices, filled := AccumulateSlices(lots, 0.5)
	if len(slices) != 1 || slices[0].LotID != 1 || !nearlyEqual(slices[0].Qty, 0.5) || !nearlyEqual(filled, 0.5) {
		t.Fatalf("unexpected: slices=%+v filled=%v", slices, filled)
	}
}

func TestAccumulateSlices_NotEnoughInventory(t *testing.T) {
	lots := []strategy.LotEntry{
		{ID: 1, Qty: 0.2},
		{ID: 2, Qty: 0.1},
	}
	slices, filled := AccumulateSlices(lots, 1.0)
	// Should consume everything and report partial fill.
	if len(slices) != 2 {
		t.Fatalf("want 2 slices, got %d", len(slices))
	}
	if !nearlyEqual(filled, 0.3) {
		t.Fatalf("filled: want 0.3, got %v", filled)
	}
}

func TestAccumulateSlices_ZeroOrNegative(t *testing.T) {
	lots := []strategy.LotEntry{{ID: 1, Qty: 1.0}}
	if s, f := AccumulateSlices(lots, 0); len(s) != 0 || f != 0 {
		t.Fatalf("zero target: want empty, got %+v %v", s, f)
	}
	if s, f := AccumulateSlices(lots, -5); len(s) != 0 || f != 0 {
		t.Fatalf("negative target: want empty, got %+v %v", s, f)
	}
}
