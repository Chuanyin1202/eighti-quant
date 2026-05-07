package ga

import (
	"math"
	"time"
)

// GhostDCAParams holds the baseline simulator's user-supplied parameters.
// They mirror the strategy macro engine's spawn-point fields so the
// baseline executes under identical constraints (per docs/20- §3.2).
type GhostDCAParams struct {
	InitialCapitalUSDT float64 // seed capital, fully deployed at first bar
	MonthlyInjectUSDT  float64 // monthly cash injection
	MinOrderUSDT       float64 // exchange minimum order size
	IntervalHours      int     // bar aggregation hours (e.g. 4)
	DeadlineDays       int     // end-of-month deadline (default 28)
}

// DefaultGhostDCAParams returns canonical config: 4h interval, 28-day deadline.
func DefaultGhostDCAParams() GhostDCAParams {
	return GhostDCAParams{IntervalHours: 4, DeadlineDays: 28}
}

// SimulateGhostDCA replays the baseline trader on a closes-only series.
//
// Iron rule: behaves IDENTICALLY to the strategy macro engine wrt cadence
// and constraints (per-tick carry, MinOrderUSDT, deadline) — any asymmetry
// gives GA a free-alpha exploit (spec §3.2).
//
// Returns:
//   roi   - Modified-Dietz-style return over the eval region
//   maxDD - peak-to-trough max drawdown over the eval region
//
// "Eval region" = bars with timestamp ≥ evalStartMs. Bars before evalStartMs
// are simulated (so monthly injections / pendingBudget are correct entering
// the region) but excluded from drawdown / ROI metrics.
func SimulateGhostDCA(closes []float64, timestamps []int64, evalStartMs int64,
	params GhostDCAParams) (roi float64, maxDD float64) {

	if len(closes) == 0 || len(closes) != len(timestamps) {
		return 0, 0
	}

	barsPerDay := 24 / params.IntervalHours
	if barsPerDay < 1 {
		barsPerDay = 1
	}

	// Account state.
	usdt := params.InitialCapitalUSDT
	asset := 0.0
	currentMonth := ""
	monthlyBudget := 0.0
	spentThisMonth := 0.0
	pendingBudget := 0.0

	// Eval-region tracking.
	evalStarted := false
	var evalStartNAV float64
	var navPeak float64
	maxDDLocal := 0.0
	evalInjected := 0.0           // total injections inside eval region
	evalWeightedInjected := 0.0   // for Modified Dietz
	var evalLastIdx int

	// Initial bar: deploy seed capital.
	if closes[0] > 0 {
		asset = usdt / closes[0]
		usdt = 0
	}

	for i, close := range closes {
		ts := timestamps[i]
		bartime := time.UnixMilli(ts).UTC()
		ym := bartime.Format("2006-01")
		dayOfMonth := bartime.Day()
		daysInMonth := lastDayOfMonth(bartime)
		barOfDay := bartime.Hour() / params.IntervalHours

		// Cross-month bookkeeping. Skip i == 0: that's seed capital, not
		// "monthly inject" — already counted as begin equity.
		if ym != currentMonth {
			if i > 0 {
				usdt += params.MonthlyInjectUSDT
				// Track inside eval region for Modified Dietz.
				if evalStarted {
					evalInjected += params.MonthlyInjectUSDT
					// Weighted by remaining-time-in-region.
					evalWeightedInjected += params.MonthlyInjectUSDT *
						weightFactor(i, len(closes), evalStartIdxOrZero(timestamps, evalStartMs))
				}
			}
			currentMonth = ym
			monthlyBudget = params.MonthlyInjectUSDT
			spentThisMonth = 0
		}

		// Per-tick growth (skip first bar).
		if i > 0 {
			remainingDays := daysInMonth - (dayOfMonth - 1)
			remainingBarsToday := barsPerDay - barOfDay
			remainingBars := (remainingDays-1)*barsPerDay + remainingBarsToday
			if remainingBars < 1 {
				remainingBars = 1
			}
			remainingBudget := monthlyBudget - spentThisMonth - pendingBudget
			if remainingBudget < 0 {
				remainingBudget = 0
			}
			pendingBudget += remainingBudget / float64(remainingBars)
		}

		// Fire decision.
		isDeadline := dayOfMonth >= params.DeadlineDays
		switch {
		case isDeadline:
			available := monthlyBudget - spentThisMonth
			orderUSD := available
			if orderUSD > usdt {
				orderUSD = usdt
			}
			if orderUSD >= params.MinOrderUSDT && close > 0 {
				asset += orderUSD / close
				usdt -= orderUSD
				spentThisMonth += orderUSD
				pendingBudget = available - orderUSD
			}

		case pendingBudget >= params.MinOrderUSDT:
			orderUSD := pendingBudget
			if orderUSD > usdt {
				orderUSD = usdt
			}
			if orderUSD >= params.MinOrderUSDT && close > 0 {
				asset += orderUSD / close
				usdt -= orderUSD
				spentThisMonth += orderUSD
				pendingBudget -= orderUSD
			}
		}

		// Eval region NAV tracking.
		if !evalStarted && ts >= evalStartMs {
			evalStarted = true
			evalStartNAV = usdt + asset*close
			navPeak = evalStartNAV
		}
		if evalStarted {
			nav := usdt + asset*close
			if nav > navPeak {
				navPeak = nav
			}
			if navPeak > 0 {
				dd := (navPeak - nav) / navPeak
				if dd > maxDDLocal {
					maxDDLocal = dd
				}
			}
			evalLastIdx = i
		}
	}

	maxDD = math.Min(maxDDLocal, 1.0)

	if !evalStarted || evalStartNAV <= 0 {
		return 0, maxDD
	}

	// Modified Dietz over the eval region.
	endNAV := usdt + asset*closes[evalLastIdx]
	denom := evalStartNAV + evalWeightedInjected
	if denom <= 0 {
		return 0, maxDD
	}
	roi = (endNAV - evalStartNAV - evalInjected) / denom
	return roi, maxDD
}

// weightFactor returns (n - i) / (n - evalStart) — the fraction of the eval
// region this injection covers. Used in Modified Dietz to give partial credit
// to mid-region injections.
func weightFactor(i, n, evalStart int) float64 {
	if n-evalStart <= 0 {
		return 0
	}
	return float64(n-1-i) / float64(n-1-evalStart)
}

// evalStartIdxOrZero finds the first index where ts >= evalStartMs.
func evalStartIdxOrZero(timestamps []int64, evalStartMs int64) int {
	for i, ts := range timestamps {
		if ts >= evalStartMs {
			return i
		}
	}
	return 0
}

// lastDayOfMonth returns 28..31 for the given UTC time.
func lastDayOfMonth(t time.Time) int {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, 0).Add(-time.Hour)
	return last.Day()
}

// MaxDrawdown computes peak-to-trough max drawdown of a NAV series.
// Exposed for the strategy simulator so both ghost DCA and the strategy
// harness use identical drawdown semantics.
func MaxDrawdown(navSeries []float64) float64 {
	if len(navSeries) < 2 {
		return 0
	}
	peak := navSeries[0]
	maxDD := 0.0
	for _, nav := range navSeries {
		if nav > peak {
			peak = nav
		}
		if peak > 0 {
			dd := (peak - nav) / peak
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return math.Min(maxDD, 1.0)
}
