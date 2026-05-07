package lunarspotv1

import (
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// Macro engine is a per-tick-carry DCA accumulator.
//
// Spec: docs/strategies/lunar-spot-v1.md §7.
//
// The big picture: each 4h bar's nominal "spend" is way below the exchange
// minimum (e.g. 600 USDT/month / 186 ticks ≈ 3.2 USDT/tick), so we accumulate
// into PendingMacroBudget until the chunk crosses MinOrderUSDT. End-of-month
// deadline is the safety valve that empties the bucket before the budget
// expires.
//
// Iron rule: idempotent guard MUST be the very first thing — no mutation
// of the runtime state before the early-return check.

const barsPerDay = 6 // 24h / 4h ; matches Manifest.AggregationInterval="4h"

// macroDecide is a pure function: given a runtime snapshot + inputs, it
// produces the new runtime state and (optionally) one BUY intent.
//
// Inputs intentionally minimal — no SaaS-side dependencies, so this can be
// unit-tested standalone with zero scaffolding.
//
// LastMacroBuyBarTime semantics: despite the legacy name, this field tracks
// "the bar this engine has already processed", NOT just "the bar we last
// fired on". It is bumped at the bottom of every successful invocation
// regardless of whether an intent was emitted, so the idempotent guard can
// still short-circuit a same-bar replay that would otherwise re-accumulate
// PendingMacroBudget.
func macroDecide(
	prev MacroRuntimeState,
	latestBarTimeMs int64,
	spendableUSDT float64,
	regime Regime,
	chromo Chromosome,
	monthlyInjectUSDT float64,
	minOrderUSDT float64,
) (next MacroRuntimeState, intent *strategy.TradeIntent) {

	next = prev

	// ── Step 0: idempotent guard (BEFORE any mutation) ────────────────────
	if prev.LastMacroBuyBarTime == latestBarTimeMs {
		return prev, nil // explicit prev to make zero-mutation obvious
	}

	// ── Step 1: derive calendar fields from the bar's CLOSE timestamp ─────
	bartime := time.UnixMilli(latestBarTimeMs).UTC()
	yearMonth := bartime.Format("2006-01")
	dayOfMonth := bartime.Day()
	daysInMonth := lastDayOfMonth(bartime)
	barOfDay := bartime.Hour() / 4 // 0..5 for 4h bars

	// ── Step 2: cross-month bookkeeping ───────────────────────────────────
	if next.CurrentMonthYearMonth != yearMonth {
		next.MonthlyBudget = monthlyInjectUSDT
		next.SpentThisMonth = 0
		// PendingMacroBudget intentionally NOT reset — it carries forward.
		next.CurrentMonthYearMonth = yearMonth
	}

	// ── Step 3: per-tick budget growth ────────────────────────────────────
	remainingDays := daysInMonth - (dayOfMonth - 1)
	remainingBarsToday := barsPerDay - barOfDay
	remainingBars := (remainingDays-1)*barsPerDay + remainingBarsToday
	if remainingBars < 1 {
		remainingBars = 1
	}
	remainingBudget := next.MonthlyBudget - next.SpentThisMonth - next.PendingMacroBudget
	if remainingBudget < 0 {
		remainingBudget = 0
	}
	perTickBase := remainingBudget / float64(remainingBars)

	accelMul := regime.TimeDilation
	if regime.State == SJMPanic {
		accelMul *= chromo.MacroAccelerator
	}
	next.PendingMacroBudget += perTickBase * accelMul

	// ── Step 4: decide whether to fire ────────────────────────────────────
	isDeadline := dayOfMonth >= chromo.MacroDeadlineDays

	switch {
	case isDeadline:
		// Spend whatever fits in the available USDT, but don't ever exceed
		// the month's leftover budget. Any unfilled portion carries forward
		// to next month's PendingMacroBudget (NOT erased — see review fix).
		availableThisMonth := next.MonthlyBudget - next.SpentThisMonth
		orderUSD := availableThisMonth
		if orderUSD > spendableUSDT {
			orderUSD = spendableUSDT
		}
		if orderUSD < minOrderUSDT {
			// Couldn't afford even the exchange minimum. Don't trade; let
			// the full `availableThisMonth` survive in PendingMacroBudget
			// so next month inherits the unspent share.
			next.LastMacroBuyBarTime = latestBarTimeMs // mark bar processed
			return next, nil
		}
		next.SpentThisMonth += orderUSD
		next.PendingMacroBudget = availableThisMonth - orderUSD
		next.LastMacroBuyBarTime = latestBarTimeMs
		return next, &strategy.TradeIntent{
			Action:     "BUY",
			Engine:     "MACRO",
			LotType:    "DEAD",
			AmountUSDT: orderUSD,
		}

	case next.PendingMacroBudget >= minOrderUSDT:
		// Normal fire path: spend the accumulated bucket, capped by Spendable
		// (so we don't try to exceed the actual cash on hand).
		orderUSD := next.PendingMacroBudget
		if orderUSD > remainingBudget+next.PendingMacroBudget {
			orderUSD = remainingBudget + next.PendingMacroBudget
		}
		if orderUSD > spendableUSDT {
			orderUSD = spendableUSDT
		}
		if orderUSD < minOrderUSDT {
			// Spendable can't cover even MinOrder. Accumulate further; mark
			// the bar processed so a same-bar replay can't double-accumulate.
			next.LastMacroBuyBarTime = latestBarTimeMs
			return next, nil
		}
		next.SpentThisMonth += orderUSD
		next.PendingMacroBudget -= orderUSD
		next.LastMacroBuyBarTime = latestBarTimeMs
		return next, &strategy.TradeIntent{
			Action:     "BUY",
			Engine:     "MACRO",
			LotType:    "DEAD",
			AmountUSDT: orderUSD,
		}

	default:
		// Below the trigger threshold; the accumulator continues to grow
		// across subsequent ticks. Mark this bar processed so a replay
		// can't re-accumulate PendingMacroBudget.
		next.LastMacroBuyBarTime = latestBarTimeMs
		return next, nil
	}
}

// lastDayOfMonth returns 28..31, matching the calendar-correct day count
// for the given bar's month/year.
func lastDayOfMonth(t time.Time) int {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, 0).Add(-time.Hour)
	return last.Day()
}
