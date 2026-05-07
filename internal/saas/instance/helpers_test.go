package instance

import (
	"testing"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

func TestBuildClientOrderID_Format(t *testing.T) {
	cid := buildClientOrderID(42, strategy.TradeIntent{
		Action: "BUY", Engine: "MACRO",
	}, 1714521600000, 0)
	want := "inst42-M-B-1714521600000-0"
	if cid != want {
		t.Fatalf("want %s, got %s", want, cid)
	}
}

func TestBuildClientOrderID_Micro(t *testing.T) {
	cid := buildClientOrderID(7, strategy.TradeIntent{
		Action: "SELL", Engine: "MICRO",
	}, 1, 3)
	if cid != "inst7-m-S-1-3" {
		t.Fatalf("got %s", cid)
	}
}

func TestBuildClientOrderID_GridDCA(t *testing.T) {
	cid := buildClientOrderID(99, strategy.TradeIntent{
		Action: "BUY", Engine: "GRID",
	}, 5, 2)
	if cid != "inst99-G-B-5-2" {
		t.Fatalf("got %s", cid)
	}
}

func TestParseIntervalHours(t *testing.T) {
	cases := map[string]int{
		"1h":  1,
		"4h":  4,
		"1d":  24,
		"15m": 0, // unsupported → 0
		"":    0,
	}
	for in, want := range cases {
		if got := parseIntervalHours(in); got != want {
			t.Fatalf("parseIntervalHours(%q): want %d, got %d", in, want, got)
		}
	}
}

func TestAlignToBarBoundary(t *testing.T) {
	// 4h interval = 14_400_000 ms
	intervalMs := int64(4 * 60 * 60 * 1000)
	cases := []struct {
		now  int64
		want int64
	}{
		{0, 0},
		{intervalMs - 1, 0},                    // mid-bar → floor to bar 0
		{intervalMs, intervalMs},               // exactly at boundary
		{intervalMs + 5_000, intervalMs},       // a few seconds into the new bar
		{2*intervalMs + 100, 2 * intervalMs},
	}
	for _, c := range cases {
		if got := alignToBarBoundary(c.now, intervalMs); got != c.want {
			t.Fatalf("alignToBarBoundary(%d): want %d, got %d", c.now, c.want, got)
		}
	}
}

func TestAlignToBarBoundary_ZeroInterval(t *testing.T) {
	if got := alignToBarBoundary(123_456, 0); got != 0 {
		t.Fatalf("zero interval should return 0, got %d", got)
	}
}

func TestAbbrevEngine(t *testing.T) {
	cases := map[string]string{
		"MACRO":  "M",
		"MICRO":  "m",
		"DCA":    "D",
		"GRID":   "G",
		"OTHER":  "O", // unknown → first letter upper
		"":       "?",
	}
	for in, want := range cases {
		if got := abbrevEngine(in); got != want {
			t.Fatalf("abbrevEngine(%q): want %q, got %q", in, want, got)
		}
	}
}
