package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/broker"
	"github.com/Chuanyin1202/eighti-quant/internal/broker/priceprovider"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Manager owns per-Instance lifecycle: state transitions, the Tick flow
// (Phase A safety gates → B Step → C atomic persistence → D dispatch),
// and BLOCKED auto-recovery. Cron Scheduler invokes Manager.Tick.
type Manager struct {
	db     *store.DB
	rds    *store.Redis
	prices priceprovider.Provider
	paper  broker.Broker // C1 SimplePaperBroker for now; live broker arrives Phase 7
	log    *zap.Logger
}

// NewManager constructs the Manager. paperBroker is required for paper-mode
// instances; pass nil only if your deployment will never run paper instances
// (very rare for this product).
func NewManager(db *store.DB, rds *store.Redis, prices priceprovider.Provider,
	paper broker.Broker, log *zap.Logger) *Manager {
	if log == nil {
		log = zap.NewNop()
	}
	return &Manager{db: db, rds: rds, prices: prices, paper: paper, log: log}
}

// Tick runs ONE evaluation cycle for one instance. Cron Scheduler invokes
// this for every RUNNING + BLOCKED instance on every minute boundary.
//
// Lifecycle (matches docs/00- §6.2):
//
//	Phase A: safety gates
//	  1. Load instance row + portfolio + runtime
//	  2. loadChampion → ErrNoChampion → mark BLOCKED + audit + return
//	     (BLOCKED instances also reach here; champion probe = recovery path)
//	  3. Same-bar dedup via PortfolioState.LastProcessedBarTime
//	  4. Pending precheck (skipped for paper — paper orders are atomic)
//	  5. (Lot vs balance check — Phase 7+ for live mode)
//
//	Phase B: Step()
//	  6. Build StrategyInput (closes from market_data_points + live ticker)
//	  7. Invoke strategy.Step()
//
//	Phase C: atomic persistence (single TX)
//	  8. Translate Intents → TradeCommands with client_order_ids
//	  9. Resolve ReleaseIntent.RelatedIntentIndex → client_order_id map
//	  10. Insert pending SpotExecution rows
//	  11. Persist updated runtime
//	  12. Apply Releases (DEAD↔FLOAT lot type changes)
//	  13. Insert audit log row(s)
//	  14. Stamp PortfolioState.LastProcessedBarTime
//
//	Phase D: dispatch
//	  15. paper: PaperBroker.PlaceOrder → mark filled, update balances/lots
//	  15. live (Phase 7): WS dispatch via WS Hub; reconcile next tick
//
// Errors that bubble up from a Phase return are logged + the tick is aborted.
// We never partial-commit: Phase C is one TX; Phase A/B/D side-effects are
// retried on the next tick.
func (m *Manager) Tick(ctx context.Context, instanceID uint) error {
	inst, port, runtime, err := m.loadInstanceState(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("tick %d: load state: %w", instanceID, err)
	}
	if inst.State == "STOPPED" || inst.State == "ERROR" {
		// Cron should already filter these out, but guard anyway.
		return nil
	}

	// ── Phase A.2: champion probe (also the BLOCKED → RUNNING wake-up path). ──
	champion, err := LoadChampion(ctx, m.db, m.rds, inst.StrategyID, inst.Symbol)
	if err != nil {
		if err == ErrNoChampion {
			return m.markBlocked(ctx, inst)
		}
		return fmt.Errorf("tick %d: load champion: %w", instanceID, err)
	}

	// If the instance was BLOCKED but champion is now available, transition
	// back to RUNNING. (Promote's post-commit wake-up usually beats us to
	// this, but the cron probe is the belt-and-suspenders backup.)
	if inst.State == "BLOCKED" {
		if err := m.transitionState(ctx, inst, "RUNNING", "champion_available"); err != nil {
			return fmt.Errorf("tick %d: blocked → running: %w", instanceID, err)
		}
		inst.State = "RUNNING"
	}

	// Strategy lookup from registry. The framework rejects instance creation
	// for unknown strategies, so a missing entry here is an internal error.
	strat, ok := strategy.Get(inst.StrategyID)
	if !ok {
		return fmt.Errorf("tick %d: strategy %q not registered", instanceID, inst.StrategyID)
	}
	manifest := strat.Manifest()

	// ── Phase A.3: same-bar dedup. ──
	intervalMs := int64(parseIntervalHours(manifest.AggregationInterval)) * int64(60*60*1000)
	if intervalMs <= 0 {
		return fmt.Errorf("tick %d: invalid AggregationInterval %q", instanceID, manifest.AggregationInterval)
	}
	latestBarTimeMs := alignToBarBoundary(time.Now().UTC().UnixMilli(), intervalMs)
	if latestBarTimeMs <= port.LastProcessedBarTime {
		// Already processed this bar (or no new bar yet).
		return nil
	}

	// ── Phase B: build StrategyInput, run Step(). ──
	closes, timestamps, err := m.loadCloses(ctx, inst.Symbol, manifest.AggregationInterval, manifest.MinWarmupBars)
	if err != nil {
		return fmt.Errorf("tick %d: load closes: %w", instanceID, err)
	}
	if len(closes) < manifest.MinWarmupBars {
		// Warmup-shy → no Step(); just stamp the bar to avoid retry storms.
		return m.stampBarProcessed(ctx, inst.ID, latestBarTimeMs)
	}

	livePrice, err := m.prices.Get(ctx, inst.Symbol)
	if err != nil {
		return fmt.Errorf("tick %d: get live price: %w", instanceID, err)
	}

	params, err := decodeParamsForStrategy(strat, champion.ParamPackJSON)
	if err != nil {
		return fmt.Errorf("tick %d: decode params: %w", instanceID, err)
	}

	lots, err := m.loadLots(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("tick %d: load lots: %w", instanceID, err)
	}

	input := strategy.StrategyInput{
		NowMs:           time.Now().UTC().UnixMilli(),
		LatestBarTimeMs: latestBarTimeMs,
		Closes:          closes,
		Timestamps:      timestamps,
		LivePrice:       livePrice,
		Portfolio: strategy.PortfolioSnapshot{
			USDTBalance:     port.USDTBalance,
			DeadAsset:       port.DeadAsset,
			FloatAsset:      port.FloatAsset,
			ColdSealedAsset: port.ColdSealedAsset,
			Lots:            lots,
		},
		Runtime:     runtime,
		Symbol:      inst.Symbol,
		LotStepSize: 0.00001, // TODO: per-symbol from a static lookup table
		LotMinQty:   0.00001,
		MinOrderUSD: 10.1,
	}

	output := strat.Step(input, params)

	// ── Phase C: atomic persistence + Phase D dispatch (paper-only for now). ──
	if err := m.persistAndDispatch(ctx, inst, latestBarTimeMs, livePrice, output); err != nil {
		return fmt.Errorf("tick %d: persist+dispatch: %w", instanceID, err)
	}

	return nil
}

// loadInstanceState fetches the three rows the tick needs in one go.
func (m *Manager) loadInstanceState(ctx context.Context, instanceID uint) (
	*store.StrategyInstance, *store.PortfolioState, strategy.RuntimeState, error) {

	var inst store.StrategyInstance
	if err := m.db.WithContext(ctx).First(&inst, instanceID).Error; err != nil {
		return nil, nil, nil, err
	}
	var port store.PortfolioState
	err := m.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&port).Error
	if err == gorm.ErrRecordNotFound {
		// First-tick instance — initialize a zero-balance row so subsequent
		// ticks read a stable state.
		port = store.PortfolioState{InstanceID: instanceID, UpdatedAt: time.Now().UTC()}
		if err := m.db.WithContext(ctx).Create(&port).Error; err != nil {
			return nil, nil, nil, err
		}
	} else if err != nil {
		return nil, nil, nil, err
	}

	var rs store.RuntimeStateRow
	err = m.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&rs).Error
	var runtime strategy.RuntimeState
	switch {
	case err == gorm.ErrRecordNotFound:
		runtime = nil // strategy decodes nil → zero state
	case err != nil:
		return nil, nil, nil, err
	default:
		// Strategies decode their own RuntimeState from a generic map.
		var raw map[string]any
		_ = jsonUnmarshal([]byte(rs.StateJSON), &raw)
		runtime = raw
	}
	return &inst, &port, runtime, nil
}

// markBlocked transitions an instance to BLOCKED state (no champion available)
// and writes the audit trail. Idempotent: re-blocking an already-BLOCKED
// instance is a cheap no-op.
func (m *Manager) markBlocked(ctx context.Context, inst *store.StrategyInstance) error {
	if inst.State == "BLOCKED" {
		return nil
	}
	return m.transitionState(ctx, inst, "BLOCKED", "no_champion")
}

func (m *Manager) transitionState(ctx context.Context, inst *store.StrategyInstance,
	to, reason string) error {

	now := time.Now().UTC().UnixMilli()
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.StrategyInstance{}).
			Where("id = ?", inst.ID).
			Update("state", to).Error; err != nil {
			return err
		}
		payload, _ := jsonMarshal(map[string]any{
			"instance_id": inst.ID,
			"from":        inst.State,
			"to":          to,
			"reason":      reason,
		})
		return tx.Create(&store.AuditLog{
			InstanceID:  inst.ID,
			EventType:   "instance_state_transition",
			PayloadJSON: string(payload),
			CreatedAtMs: now,
		}).Error
	})
	if err == nil {
		inst.State = to
	}
	return err
}

// stampBarProcessed updates LastProcessedBarTime so warm-up retries don't
// pile up. No other state changes — this is the "we noticed the bar but
// didn't act" path.
func (m *Manager) stampBarProcessed(ctx context.Context, instanceID uint, barTime int64) error {
	return m.db.WithContext(ctx).Model(&store.PortfolioState{}).
		Where("instance_id = ?", instanceID).
		Updates(map[string]any{
			"last_processed_bar_time": barTime,
			"updated_at":              time.Now().UTC(),
		}).Error
}
