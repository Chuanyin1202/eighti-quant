package lunarspotv1

import (
	"math"

	"github.com/Chuanyin1202/eighti-quant/internal/quant"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// microInput holds the inputs to a single micro-engine evaluation.
type microInput struct {
	pva                PVA
	regime             Regime
	currentMicroWeight float64
	totalEquity        float64
	floatAssetQty      float64
	livePrice          float64
	minOrderUSDT       float64
	chromo             Chromosome
}

// microOutput is the micro-engine result. NeedsHardRelease indicates that
// the SELL exceeds available FloatAsset; release.go decides how to satisfy
// the deficit (or not) and the strategy.Step main flow stitches the pair
// together with the right RelatedIntentIndex.
type microOutput struct {
	Intent           *strategy.TradeIntent // nil if no order
	NeedsHardRelease bool                  // true ⇒ SELL but FloatAsset < |OrderUSD|
	HardReleaseQty   float64               // qty to convert DEAD→FLOAT (only meaningful when NeedsHardRelease)
	Diagnostics      map[string]float64
}

// microDecide runs the Sigmoid dynamic balance and applies the wedge filter.
//
// Returns nil intent if the strategy decides not to trade (PVA NaN, dust
// below wedge threshold, or quiet regime gating).
func microDecide(in microInput) microOutput {
	out := microOutput{Diagnostics: map[string]float64{}}

	// Warm-up sanity: PVA components might be NaN if the close history is
	// shorter than what the chromosome's lookback windows expect.
	if math.IsNaN(in.pva.P) || math.IsNaN(in.pva.V) || math.IsNaN(in.pva.A) {
		out.Diagnostics["pva_nan"] = 1
		return out
	}

	signal := in.chromo.SignalW_P*in.pva.P +
		in.chromo.SignalW_V*in.pva.V +
		in.chromo.SignalW_A*in.pva.A

	bal := quant.ComputeDynamicBalance(quant.DynamicBalanceInput{
		Signal:             signal,
		Beta:               in.chromo.Beta,
		BetaMultiplier:     in.regime.BetaMultiplier,
		Gamma:              in.chromo.Gamma,
		CurrentMicroWeight: in.currentMicroWeight,
		TotalEquity:        in.totalEquity,
	})
	out.Diagnostics["signal"] = signal
	out.Diagnostics["target_weight"] = bal.TargetWeight
	out.Diagnostics["delta_weight"] = bal.DeltaWeight

	// Wedge filter: dust orders below MinOrderUSDT are dropped except in a
	// non-quiet regime when the desired weight delta crosses the chromosome
	// threshold (the strategy's "this is meaningful, force a min-size order"
	// override).
	abs := math.Abs(bal.TheoreticalUSD)
	switch {
	case abs >= in.minOrderUSDT:
		// Direct fire — order at theoretical size.
		out.Intent = makeMicroIntent(bal.TheoreticalUSD, in.livePrice)

	case abs > 0 && !in.regime.IsQuiet && math.Abs(bal.DeltaWeight) >= in.chromo.WedgeDeltaThr:
		// Wedge override: scale up to MinOrderUSDT, preserving sign.
		sign := 1.0
		if bal.TheoreticalUSD < 0 {
			sign = -1.0
		}
		out.Intent = makeMicroIntent(sign*in.minOrderUSDT, in.livePrice)

	default:
		// Skip dust silently.
		return out
	}

	// Hard-release detection: if SELL exceeds FloatAsset's USD value, we
	// need to convert DEAD → FLOAT before the SELL can execute. Compute
	// the deficit qty here; release.go decides if it's actually feasible.
	if out.Intent != nil && out.Intent.Action == "SELL" {
		needQty := out.Intent.QtyAsset
		if needQty > in.floatAssetQty {
			out.NeedsHardRelease = true
			out.HardReleaseQty = needQty - in.floatAssetQty
		}
	}
	return out
}

// makeMicroIntent converts an OrderUSD signed scalar into a TradeIntent.
//
// Positive ⇒ BUY (USDT-denominated); negative ⇒ SELL (asset-qty denominated).
// We compute SELL qty using the live ticker price; the actual fill will be
// at whatever price the broker delivers.
func makeMicroIntent(orderUSD, livePrice float64) *strategy.TradeIntent {
	if orderUSD == 0 {
		return nil
	}
	if orderUSD > 0 {
		return &strategy.TradeIntent{
			Action:     "BUY",
			Engine:     "MICRO",
			LotType:    "FLOAT",
			AmountUSDT: orderUSD,
		}
	}
	qty := -orderUSD / livePrice
	return &strategy.TradeIntent{
		Action:   "SELL",
		Engine:   "MICRO",
		LotType:  "FLOAT",
		QtyAsset: qty,
	}
}
