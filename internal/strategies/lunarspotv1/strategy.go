package lunarspotv1

import (
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

type Strategy struct{}

func init() {
	strategy.Register(&Strategy{})
}

func (s *Strategy) Manifest() strategy.Manifest {
	return strategy.Manifest{
		ID:                  StrategyID,
		Name:                "雙軌量化策略 v1",
		Version:             "1.0.0",
		IsSpot:              true,
		SupportedSymbols:    []string{"BTCUSDT", "ETHUSDT"},
		RequiredDataKind:    "close-only",
		MinWarmupBars:       MinWarmupBars,
		AggregationInterval: "4h",
		SupportsEvolution:   true,
	}
}

func (s *Strategy) DefaultParams() strategy.Params {
	return Clamp(Params{
		Chromosome: DefaultChromosome(),
		SpawnPoint: DefaultSpawnPoint(),
	})
}

// Step is the main per-tick decision. Stages match docs/strategies/lunar-spot-v1.md §10:
//
//   1. warm-up gate
//   2. account accounting (TotalEquity, dual-engine shared SpendableUSDT,
//      CurrentMicroWeight)
//   3. PVA + SJM regime
//   4. macro engine (BUY-only, per-tick carry, deadline guard)
//   5. micro engine (Sigmoid balance, wedge filter, hard-release detection)
//   6. release intents (soft + hard) — soft is bookkeeping; hard is paired
//      with the matching SELL via RelatedIntentIndex
//   7. global stop-loss circuit breaker
//   8. assemble output
func (s *Strategy) Step(in strategy.StrategyInput, p strategy.Params) strategy.StrategyOutput {
	params, _ := p.(Params)
	params = Clamp(params)
	chromo := params.Chromosome
	spawn := params.SpawnPoint

	runtime := decodeRuntime(in.Runtime)

	out := strategy.StrategyOutput{
		NewRuntime:  runtime,
		Diagnostics: map[string]float64{},
	}

	// 1. Warm-up — strict gate on the longest indicator window.
	if len(in.Closes) < MinWarmupBars {
		return out
	}

	// 2. Account accounting (dual engines share TotalEquity and SpendableUSDT;
	//    no micro_reserve_pct partitioning — the spec is emphatic on this).
	totalAssetQty := in.Portfolio.DeadAsset + in.Portfolio.FloatAsset + in.Portfolio.ColdSealedAsset
	totalEquity := in.Portfolio.USDTBalance + totalAssetQty*in.LivePrice
	if totalEquity <= 0 {
		return out
	}
	spendableUSDT := in.Portfolio.USDTBalance
	if spendableUSDT < 0 {
		spendableUSDT = 0
	}
	currentMicroWeight := 0.0
	if in.LivePrice > 0 {
		currentMicroWeight = (in.Portfolio.FloatAsset * in.LivePrice) / totalEquity
	}

	// 7'. Global stop-loss BEFORE engines run — never produce new orders if
	//     equity dropped below the configured threshold.
	if spawn.GlobalStopLossPct > 0 && spawn.InitialCapitalUSDT > 0 {
		stopAt := spawn.InitialCapitalUSDT * (1 - spawn.GlobalStopLossPct)
		if totalEquity <= stopAt {
			out.Diagnostics["stop_loss_halted"] = 1
			return out
		}
	}

	// 3. PVA + SJM regime.
	pva := computePVA(in.Closes, chromo)
	regime := classifySJM(in.Closes, pva, chromo)
	out.Diagnostics["regime_panic"] = boolToFloat(regime.State == SJMPanic)
	out.Diagnostics["regime_greed"] = boolToFloat(regime.State == SJMGreed)
	out.Diagnostics["regime_quiet"] = boolToFloat(regime.State == SJMQuiet)

	// 4. Macro engine — emit BUY (or nothing).
	newMacroState, macroIntent := macroDecide(
		runtime.Macro,
		in.LatestBarTimeMs,
		spendableUSDT,
		regime,
		chromo,
		spawn.MonthlyInjectUSDT,
		in.MinOrderUSD,
	)
	runtime.Macro = newMacroState

	// 5. Micro engine — Sigmoid + wedge.
	micro := microDecide(microInput{
		pva:                pva,
		regime:             regime,
		currentMicroWeight: currentMicroWeight,
		totalEquity:        totalEquity,
		floatAssetQty:      in.Portfolio.FloatAsset,
		livePrice:          in.LivePrice,
		minOrderUSDT:       in.MinOrderUSD,
		chromo:             chromo,
	})
	for k, v := range micro.Diagnostics {
		out.Diagnostics[k] = v
	}

	// 6. Releases.
	//    Soft release: independent of SELL, RelatedIntentIndex = -1.
	//    Hard release: paired with the micro SELL we are about to emit.
	//                  Must compute the SELL's index in out.Intents BEFORE
	//                  appending the SELL, so the index ends up correct.
	soft := maybeSoftRelease(
		in.Portfolio.Lots, currentMicroWeight, totalEquity, in.LivePrice,
		chromo, in.LatestBarTimeMs,
	)

	// 8. Assemble intents in deterministic order: macro first, micro second.
	//    The SELL's index in out.Intents is what RelatedIntentIndex must be.
	if macroIntent != nil {
		out.Intents = append(out.Intents, *macroIntent)
	}
	if micro.Intent != nil {
		microIntentIdx := len(out.Intents) // index it WILL get when appended
		out.Intents = append(out.Intents, *micro.Intent)

		// Hard release pairing: only if the micro intent is a SELL with deficit.
		if micro.NeedsHardRelease {
			if hard := maybeHardRelease(in.Portfolio.Lots, micro.HardReleaseQty, microIntentIdx); hard != nil {
				out.Releases = append(out.Releases, *hard)
			}
		}
	}
	if soft != nil {
		out.Releases = append(out.Releases, *soft)
	}

	out.NewRuntime = runtime
	return out
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
