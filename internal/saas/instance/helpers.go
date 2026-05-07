package instance

import (
	"context"
	"fmt"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/Chuanyin1202/eighti-quant/internal/strategies/grid"
	"github.com/Chuanyin1202/eighti-quant/internal/strategies/lunarspotv1"
	"github.com/Chuanyin1202/eighti-quant/internal/strategies/simpledca"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
)

// loadCloses returns the most-recent N close-price points for the given
// (symbol, interval). N defaults to enough to satisfy MinWarmupBars + a
// small extra buffer. Bars are returned ascending by OpenTime.
func (m *Manager) loadCloses(ctx context.Context, symbol, interval string, minBars int) ([]float64, []int64, error) {
	limit := minBars + 32 // generous buffer above warm-up
	var rows []store.MarketDataPoint
	err := m.db.WithContext(ctx).
		Where("symbol = ? AND interval = ?", symbol, interval).
		Order("open_time DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	closes := make([]float64, len(rows))
	timestamps := make([]int64, len(rows))
	for i, r := range rows {
		// We pulled DESC; reverse into ascending.
		closes[len(rows)-1-i] = r.Close
		timestamps[len(rows)-1-i] = r.OpenTime
	}
	return closes, timestamps, nil
}

// loadLots returns the per-lot detail for an instance (used by hard-release
// inside Step()). Includes COLD_SEALED so the strategy can see them, but
// release.go MUST not touch IsColdSealed=true entries.
func (m *Manager) loadLots(ctx context.Context, instanceID uint) ([]strategy.LotEntry, error) {
	var rows []store.SpotLot
	if err := m.db.WithContext(ctx).
		Where("instance_id = ? AND qty > 0", instanceID).
		Order("buy_time_ms ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]strategy.LotEntry, len(rows))
	for i, r := range rows {
		out[i] = strategy.LotEntry{
			ID:           r.ID,
			Type:         r.Type,
			Qty:          r.Qty,
			BuyPriceUSDT: r.BuyPriceUSDT,
			BuyTimeMs:    r.BuyTimeMs,
			IsColdSealed: r.IsColdSealed,
		}
	}
	return out, nil
}

// decodeParamsForStrategy invokes the strategy-specific DecodeParams helper.
//
// The strategy SDK's Strategy interface deliberately doesn't expose Decode —
// it would force every strategy to handle JSON. We instead switch on the
// known reference IDs here. New evolvable strategies need to be wired in
// here too; the alternative (a registry of decoder fns) is more elegant
// but adds another piece of boilerplate at strategy definition time.
func decodeParamsForStrategy(strat strategy.Strategy, paramPackJSON []byte) (strategy.Params, error) {
	if len(paramPackJSON) == 0 {
		return strat.DefaultParams(), nil
	}
	switch strat.Manifest().ID {
	case grid.StrategyID:
		p, err := grid.DecodeParams(paramPackJSON)
		if err != nil {
			return strat.DefaultParams(), fmt.Errorf("grid decode: %w", err)
		}
		return strategy.Params(p), nil
	case lunarspotv1.StrategyID:
		p, err := lunarspotv1.DecodeParams(paramPackJSON)
		if err != nil {
			return strat.DefaultParams(), fmt.Errorf("lunar decode: %w", err)
		}
		return strategy.Params(p), nil
	case simpledca.StrategyID:
		// simple-dca isn't evolvable; champion JSON shouldn't arrive at all.
		// If it does, prefer DefaultParams over a flaky parse.
		return strat.DefaultParams(), nil
	}
	// Unknown strategy: fall back to defaults so a misconfiguration doesn't
	// crash the cron tick.
	return strat.DefaultParams(), nil
}

// parseIntervalHours converts "1h" / "4h" / "1d" → integer hours.
// Returns 0 on parse failure (caller should treat as a fatal misconfig).
func parseIntervalHours(s string) int {
	switch s {
	case "1h":
		return 1
	case "4h":
		return 4
	case "1d":
		return 24
	}
	return 0
}

// alignToBarBoundary rounds nowMs DOWN to the nearest bar boundary.
//
// "Bar boundary" = the bar's CLOSE timestamp (not its open). For 4h bars
// that's 0/4/8/12/16/20 UTC; for 1d it's 0 UTC.
//
// Rationale: Step() expects LatestBarTimeMs to be the close-time of the
// most recently completed bar. We compute it by floor()'ing nowMs to the
// nearest interval, which is the latest open we've seen; the close = open
// + interval if and only if the current wall-clock has actually advanced
// past that close. Since the cron tick fires every minute, the "latest
// completed" close is always one interval back from the in-progress bar.
func alignToBarBoundary(nowMs, intervalMs int64) int64 {
	if intervalMs <= 0 {
		return 0
	}
	openOfCurrent := (nowMs / intervalMs) * intervalMs
	// Latest CLOSED bar's close = open of the current bar.
	return openOfCurrent
}
