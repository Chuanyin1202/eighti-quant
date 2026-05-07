package ga

import (
	"math"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// SimConfig is the per-window backtest configuration.
type SimConfig struct {
	InitialCapitalUSDT float64
	MonthlyInjectUSDT  float64
	ColdSealedPct      float64
	FeeRate            float64 // e.g. 0.001 (Binance spot)
	MinOrderUSDT       float64
	LotStepSize        float64
	LotMinQty          float64
	IntervalHours      int
}

// DefaultSimConfig returns conservative Binance-spot defaults; callers fill
// InitialCapital / MonthlyInject from the strategy SpawnPoint.
func DefaultSimConfig() SimConfig {
	return SimConfig{
		FeeRate:       0.001,
		MinOrderUSDT:  10.1,
		LotStepSize:   0.00001,
		LotMinQty:     0.00001,
		IntervalHours: 4,
	}
}

// SimResult is what one backtest run produces.
type SimResult struct {
	ROI         float64
	MaxDrawdown float64
	NAVStart    float64
	NAVEnd      float64
	NumOrders   int
}

// SimulateStrategy runs strategy.Step() bar-by-bar against a window's close
// series. Inputs:
//   strat       - the strategy under test
//   params      - chromosome+spawn pack to feed Step()
//   closes / ts - the window data (ascending)
//   evalStartMs - bars before this are warm-up only (no NAV/ROI tracking)
//   cfg         - simulator state
//
// The simulator maintains:
//   - mock account: USDT balance + lots ([]LotEntry)
//   - cross-month MonthlyInject (skipped on first bar — that's seed capital)
//   - cumulative cash flow for Modified Dietz ROI
//   - eval-region NAV peaks for max drawdown
//
// Trade execution model (paper-style, matches docs/30- §3 SimplePaperBroker):
//   - BUY at bar close, fee = AmountUSDT × FeeRate (paid in USDT)
//     fillQty = (AmountUSDT - fee) / closePrice
//   - SELL at bar close, fee = qty × close × FeeRate
//     received = qty × close - fee
//   - Releases (DEAD↔FLOAT lot type) are bookkeeping; no fees
//
// This MUST stay in lockstep with the live PaperBroker — same fee model,
// same order semantics — so backtest fitness predicts paper-mode behaviour.
func SimulateStrategy(strat strategy.Strategy, params strategy.Params,
	closes []float64, ts []int64, evalStartMs int64, cfg SimConfig) SimResult {

	if len(closes) == 0 || len(closes) != len(ts) {
		return SimResult{}
	}

	// Mock account.
	usdt := cfg.InitialCapitalUSDT
	var lots []strategy.LotEntry
	var nextLotID uint = 1

	// Cross-month and cash-flow tracking.
	currentMonth := ""
	evalStarted := false
	var evalStartNAV, navPeak float64
	maxDD := 0.0
	evalInjected := 0.0
	evalWeightedInjected := 0.0
	var runtime strategy.RuntimeState
	numOrders := 0

	intervalMs := int64(cfg.IntervalHours) * int64(60*60*1000)
	_ = intervalMs

	for i, close := range closes {
		barTime := ts[i]
		bartime := time.UnixMilli(barTime).UTC()
		ym := bartime.Format("2006-01")

		// Cross-month: inject monthly capital (skip first bar — seed capital
		// already in usdt).
		if ym != currentMonth {
			if i > 0 {
				usdt += cfg.MonthlyInjectUSDT
				if evalStarted {
					evalInjected += cfg.MonthlyInjectUSDT
					evalWeightedInjected += cfg.MonthlyInjectUSDT *
						weightFactor(i, len(closes), evalStartIdxOrZero(ts, evalStartMs))
				}
			}
			currentMonth = ym
		}

		// Build StrategyInput. We DO NOT support hard-release in the
		// simulator yet (Phase 5 keeps it minimal — DEAD never converts to
		// FLOAT during backtest, so SELL deficits just fall through and
		// the strategy's own logic will skip them).
		input := strategy.StrategyInput{
			NowMs:           barTime,
			LatestBarTimeMs: barTime,
			Closes:          closes[:i+1],
			Timestamps:      ts[:i+1],
			LivePrice:       close,
			Portfolio:       buildPortfolio(usdt, lots, close),
			Runtime:         runtime,
			Symbol:          "", // simulator is symbol-agnostic
			LotStepSize:     cfg.LotStepSize,
			LotMinQty:       cfg.LotMinQty,
			MinOrderUSD:     cfg.MinOrderUSDT,
		}

		out := strat.Step(input, params)
		runtime = out.NewRuntime

		// Apply Releases first (bookkeeping only — no fees, no cash flow).
		for _, rel := range out.Releases {
			applyRelease(&lots, rel)
		}

		// Apply Intents.
		for _, intent := range out.Intents {
			if applied := applyIntent(&usdt, &lots, &nextLotID, intent, close, barTime, cfg); applied {
				numOrders++
			}
		}

		// NAV tracking inside eval region.
		nav := navOf(usdt, lots, close)
		if !evalStarted && barTime >= evalStartMs {
			evalStarted = true
			evalStartNAV = nav
			navPeak = nav
		}
		if evalStarted {
			if nav > navPeak {
				navPeak = nav
			}
			if navPeak > 0 {
				dd := (navPeak - nav) / navPeak
				if dd > maxDD {
					maxDD = dd
				}
			}
		}
	}

	endNAV := navOf(usdt, lots, closes[len(closes)-1])
	maxDD = math.Min(maxDD, 1.0)

	if !evalStarted || evalStartNAV <= 0 {
		return SimResult{NumOrders: numOrders, MaxDrawdown: maxDD}
	}

	denom := evalStartNAV + evalWeightedInjected
	roi := 0.0
	if denom > 0 {
		roi = (endNAV - evalStartNAV - evalInjected) / denom
	}

	return SimResult{
		ROI:         roi,
		MaxDrawdown: maxDD,
		NAVStart:    evalStartNAV,
		NAVEnd:      endNAV,
		NumOrders:   numOrders,
	}
}

// buildPortfolio aggregates the lots into the snapshot fields the strategy
// expects. Matches the live ledger semantics so Step() doesn't see any
// simulator-only artefacts.
func buildPortfolio(usdt float64, lots []strategy.LotEntry, livePrice float64) strategy.PortfolioSnapshot {
	var dead, flt, sealed float64
	for _, l := range lots {
		switch l.Type {
		case "DEAD":
			dead += l.Qty
		case "FLOAT":
			flt += l.Qty
		case "COLD_SEALED":
			sealed += l.Qty
		}
	}
	return strategy.PortfolioSnapshot{
		USDTBalance:     usdt,
		DeadAsset:       dead,
		FloatAsset:      flt,
		ColdSealedAsset: sealed,
		Lots:            cloneLots(lots),
	}
}

func cloneLots(lots []strategy.LotEntry) []strategy.LotEntry {
	out := make([]strategy.LotEntry, len(lots))
	copy(out, lots)
	return out
}

// applyIntent mutates usdt + lots according to a BUY or SELL.
// Returns true if the order actually executed (non-dust + sufficient
// inventory / funds).
func applyIntent(usdt *float64, lots *[]strategy.LotEntry, nextLotID *uint,
	intent strategy.TradeIntent, close float64, barTime int64, cfg SimConfig) bool {

	switch intent.Action {
	case "BUY":
		amount := intent.AmountUSDT
		if amount < cfg.MinOrderUSDT {
			return false
		}
		if amount > *usdt {
			amount = *usdt
		}
		if amount < cfg.MinOrderUSDT {
			return false
		}
		fee := amount * cfg.FeeRate
		fillQty := (amount - fee) / close
		if fillQty < cfg.LotMinQty {
			return false
		}
		*lots = append(*lots, strategy.LotEntry{
			ID:           *nextLotID,
			Type:         intent.LotType,
			Qty:          fillQty,
			BuyPriceUSDT: close,
			BuyTimeMs:    barTime,
		})
		*nextLotID++
		*usdt -= amount
		return true

	case "SELL":
		need := intent.QtyAsset
		if need < cfg.LotMinQty {
			return false
		}
		// Only FLOAT lots are sellable (matches strategy contract).
		var available float64
		for _, l := range *lots {
			if l.Type == "FLOAT" && !l.IsColdSealed {
				available += l.Qty
			}
		}
		if available < cfg.LotMinQty {
			return false
		}
		if need > available {
			need = available
		}
		// Consume FIFO from FLOAT lots.
		remaining := need
		newLots := (*lots)[:0]
		for _, l := range *lots {
			if remaining <= 0 || l.Type != "FLOAT" || l.IsColdSealed {
				newLots = append(newLots, l)
				continue
			}
			if l.Qty <= remaining {
				remaining -= l.Qty
				continue // drop entirely
			}
			l.Qty -= remaining
			remaining = 0
			newLots = append(newLots, l)
		}
		*lots = append([]strategy.LotEntry(nil), newLots...)
		gross := need * close
		fee := gross * cfg.FeeRate
		*usdt += gross - fee
		return true

	default:
		return false
	}
}

// applyRelease converts source-lot Qty between types. Only DEAD ↔ FLOAT is
// supported; COLD_SEALED is never touched.
func applyRelease(lots *[]strategy.LotEntry, rel strategy.ReleaseIntent) {
	if rel.TotalQtyAsset <= 0 {
		return
	}
	// Index lots by ID for fast lookup.
	idx := make(map[uint]int, len(*lots))
	for i, l := range *lots {
		idx[l.ID] = i
	}
	for _, slice := range rel.Slices {
		j, ok := idx[slice.LotID]
		if !ok {
			continue
		}
		l := (*lots)[j]
		if l.Type != "DEAD" || l.IsColdSealed {
			continue
		}
		take := slice.Qty
		if take > l.Qty {
			take = l.Qty
		}
		// Reduce the DEAD lot.
		(*lots)[j].Qty -= take
		// Add a new FLOAT lot mirroring the cost basis + age.
		*lots = append(*lots, strategy.LotEntry{
			ID:           uint(len(*lots) + 1), // synthesized ID; OK for sim
			Type:         "FLOAT",
			Qty:          take,
			BuyPriceUSDT: l.BuyPriceUSDT,
			BuyTimeMs:    l.BuyTimeMs,
		})
	}
	// Drop any DEAD lots whose Qty went to zero.
	cleaned := (*lots)[:0]
	for _, l := range *lots {
		if l.Qty <= 0 {
			continue
		}
		cleaned = append(cleaned, l)
	}
	*lots = append([]strategy.LotEntry(nil), cleaned...)
}

func navOf(usdt float64, lots []strategy.LotEntry, livePrice float64) float64 {
	var qty float64
	for _, l := range lots {
		qty += l.Qty
	}
	return usdt + qty*livePrice
}
