// Package lunarspotv1 is the flagship reference strategy: dual-engine
// (macro DCA + micro Sigmoid balance) with PVA / SJM regime detection
// and 14 GA-evolvable chromosome fields.
//
// Spec: docs/strategies/lunar-spot-v1.md.
//
// File layout:
//   params.go    - Chromosome + SpawnPoint + Clamp + RuntimeState
//   regime.go    - PVA computation + SJM three-state classifier
//   macro.go     - DCA engine with per-tick carry balance + deadline
//   micro.go     - Sigmoid dynamic balance + wedge filter
//   release.go   - DEAD → FLOAT lot transitions (soft + hard)
//   gene.go      - GA operations (Sample/Mutate/Crossover/Fingerprint/...)
//   strategy.go  - Manifest + Step main flow
package lunarspotv1

import (
	"encoding/json"
	"math"

	"github.com/Chuanyin1202/eighti-quant/internal/quant"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

const (
	StrategyID    = "lunar-spot-v1"
	MinWarmupBars = 600 // EMA-window upper bound + sigma rolling history
)

// Chromosome holds the 14 GA-evolvable fields. Bounds and defaults match
// docs/strategies/lunar-spot-v1.md §9.1.
//
// Sign convention (recap from sigmoid.go):
//   Signal > 0 ⇒ go bearish (target weight ↓)
//   SignalW_* coefficients let the GA learn whatever sign each PVA factor
//   should carry — defaults pick W_P=+0.5, W_V=W_A=0 to make the bootstrap
//   chromosome deterministic and non-degenerate (Clamp constraint #1).
type Chromosome struct {
	Beta                   float64 `json:"beta"`
	Gamma                  float64 `json:"gamma"`
	SigmaFloor             float64 `json:"sigma_floor"`
	SignalW_P              float64 `json:"signal_w_p"`
	SignalW_V              float64 `json:"signal_w_v"`
	SignalW_A              float64 `json:"signal_w_a"`
	EMABaseBars            int     `json:"ema_base_bars"`
	SigmaWindow            int     `json:"sigma_window"`
	VelocityLookback       int     `json:"velocity_lookback"`
	WedgeDeltaThr          float64 `json:"wedge_delta_thr"`
	MacroAccelerator       float64 `json:"macro_accelerator"`
	MacroDeadlineDays      int     `json:"macro_deadline_days"`
	DeadAgingMonths        int     `json:"dead_aging_months"`
	TargetMicroWeightFloor float64 `json:"target_micro_weight_floor"`
}

// SpawnPoint is the per-instance, NON-evolving configuration. Set by the
// user at instance creation time, frozen for the GA epoch's lifetime.
type SpawnPoint struct {
	MonthlyInjectUSDT  float64 `json:"monthly_inject_usdt"`  // user budget
	InitialCapitalUSDT float64 `json:"initial_capital_usdt"` // for stop-loss baseline
	ColdSealedPct      float64 `json:"cold_sealed_pct"`      // fraction permanently sealed
	GlobalStopLossPct  float64 `json:"global_stop_loss_pct"` // halt when equity drops below this
}

// Params is the full parameter pack handed to Step(). The GA only mutates
// Chromosome; SpawnPoint round-trips unchanged.
type Params struct {
	Chromosome Chromosome `json:"chromosome"`
	SpawnPoint SpawnPoint `json:"spawn_point"`
}

// MacroRuntimeState is the persistent macro-engine state (per-tick carry).
type MacroRuntimeState struct {
	CurrentMonthYearMonth string  `json:"current_month_year_month"`
	MonthlyBudget         float64 `json:"monthly_budget"`
	SpentThisMonth        float64 `json:"spent_this_month"`
	PendingMacroBudget    float64 `json:"pending_macro_budget"`
	LastMacroBuyBarTime   int64   `json:"last_macro_buy_bar_time"`
}

// RuntimeState is what gets persisted between ticks.
type RuntimeState struct {
	Macro MacroRuntimeState `json:"macro"`
}

func decodeRuntime(raw strategy.RuntimeState) RuntimeState {
	if raw == nil {
		return RuntimeState{}
	}
	switch v := raw.(type) {
	case RuntimeState:
		return v
	case *RuntimeState:
		if v == nil {
			return RuntimeState{}
		}
		return *v
	case map[string]any:
		buf, err := json.Marshal(v)
		if err != nil {
			return RuntimeState{}
		}
		var rs RuntimeState
		_ = json.Unmarshal(buf, &rs)
		return rs
	}
	return RuntimeState{}
}

// DefaultChromosome is the canonical bootstrap seed. MUST be deterministic
// (no random fallback), MUST satisfy all Clamp constraints without modification.
//
// Spec: docs/strategies/lunar-spot-v1.md §9.4.
func DefaultChromosome() Chromosome {
	return Chromosome{
		Beta:                   1.5,
		Gamma:                  0.5,
		SigmaFloor:             0.005,
		SignalW_P:              0.5, // non-zero so Clamp constraint #1 is auto-satisfied
		SignalW_V:              0.0,
		SignalW_A:              0.0,
		EMABaseBars:            200,
		SigmaWindow:            30,
		VelocityLookback:       3,
		WedgeDeltaThr:          0.03,
		MacroAccelerator:       1.5,
		MacroDeadlineDays:      28,
		DeadAgingMonths:        6,
		TargetMicroWeightFloor: 0.15,
	}
}

// DefaultSpawnPoint is a reasonable starting configuration for a fresh user.
func DefaultSpawnPoint() SpawnPoint {
	return SpawnPoint{
		MonthlyInjectUSDT:  100.0,
		InitialCapitalUSDT: 1000.0,
		ColdSealedPct:      0.10,
		GlobalStopLossPct:  0.5,
	}
}

// Clamp enforces hard bounds, structural constraints, and integer rounding.
//
// Constraints (matches docs §9.2):
//   1. abs(W_P) + abs(W_V) + abs(W_A) >= 0.1
//      → if violated, deterministically set W_P = +0.5
//   2. integer fields: round + clamp
//   3. TargetMicroWeightFloor < 0.5 (hard ceiling at 0.49 to leave headroom)
func Clamp(p Params) Params {
	c := p.Chromosome

	c.Beta = quant.ClipFloat64(c.Beta, 0.1, 5.0)
	c.Gamma = quant.ClipFloat64(c.Gamma, 0.0, 2.0)
	c.SigmaFloor = quant.ClipFloat64(c.SigmaFloor, 0.001, 0.05)
	c.SignalW_P = quant.ClipFloat64(c.SignalW_P, -2.0, 2.0)
	c.SignalW_V = quant.ClipFloat64(c.SignalW_V, -2.0, 2.0)
	c.SignalW_A = quant.ClipFloat64(c.SignalW_A, -2.0, 2.0)
	c.WedgeDeltaThr = quant.ClipFloat64(c.WedgeDeltaThr, 0.01, 0.1)
	c.MacroAccelerator = quant.ClipFloat64(c.MacroAccelerator, 1.0, 3.0)
	c.TargetMicroWeightFloor = quant.ClipFloat64(c.TargetMicroWeightFloor, 0.05, 0.49)

	c.EMABaseBars = clampInt(int(math.Round(float64(c.EMABaseBars))), 50, 600)
	c.SigmaWindow = clampInt(int(math.Round(float64(c.SigmaWindow))), 10, 100)
	c.VelocityLookback = clampInt(int(math.Round(float64(c.VelocityLookback))), 1, 10)
	c.MacroDeadlineDays = clampInt(int(math.Round(float64(c.MacroDeadlineDays))), 25, 30)
	c.DeadAgingMonths = clampInt(int(math.Round(float64(c.DeadAgingMonths))), 3, 24)

	// Constraint #1: at least one signal weight must be active.
	if math.Abs(c.SignalW_P)+math.Abs(c.SignalW_V)+math.Abs(c.SignalW_A) < 0.1 {
		c.SignalW_P = 0.5 // deterministic fallback (NOT random, see §9.4)
	}

	p.Chromosome = c
	return p
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
