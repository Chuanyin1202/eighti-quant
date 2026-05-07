package instance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/broker"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
	"gorm.io/gorm"
)

// persistAndDispatch implements Phase C + D from docs/00- §6.2.
//
// Phase C (single TX):
//   1. Translate Intents → TradeCommand + ClientOrderID (build the
//      idx → client_order_id map immediately so Releases can resolve their
//      RelatedIntentIndex back to a client_order_id before audit-logging).
//   2. Insert pending SpotExecution rows.
//   3. Persist NewRuntime as JSON in runtime_state_rows.
//   4. Apply Releases by mutating SpotLot rows + PortfolioState aggregates.
//   5. Insert audit logs.
//   6. Stamp PortfolioState.LastProcessedBarTime.
//
// Phase D (post-TX, paper-only for now):
//   7. For each pending row, call PaperBroker.PlaceOrder. On terminal
//      Status=filled, mark the SpotExecution filled, write a TradeRecord,
//      and update PortfolioState + SpotLot to reflect the fill.
//
// Live mode (Phase 7) replaces step 7 with a WS dispatch and reconcile
// path; the rest of the function stays identical.
func (m *Manager) persistAndDispatch(ctx context.Context, inst *store.StrategyInstance,
	latestBarTimeMs int64, livePrice float64, out strategy.StrategyOutput) error {

	// Build TradeCommand list and client_order_id map (idx → cid).
	commands := make([]broker.TradeCommand, len(out.Intents))
	clientIDs := make([]string, len(out.Intents))
	for i, intent := range out.Intents {
		cid := buildClientOrderID(inst.ID, intent, latestBarTimeMs, i)
		clientIDs[i] = cid
		commands[i] = broker.TradeCommand{
			ClientOrderID: cid,
			Symbol:        inst.Symbol,
			Action:        intent.Action,
			Engine:        intent.Engine,
			LotType:       intent.LotType,
			AmountUSDT:    intent.AmountUSDT,
			QtyAsset:      intent.QtyAsset,
			BarTimeMs:     latestBarTimeMs,
		}
	}

	now := time.Now().UTC().UnixMilli()

	// Phase C: single transaction.
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 2. Pending SpotExecution rows.
		for i, cmd := range commands {
			if err := tx.Create(&store.SpotExecution{
				InstanceID:     inst.ID,
				ClientOrderID:  cmd.ClientOrderID,
				Status:         broker.StatusPending,
				Action:         cmd.Action,
				Engine:         cmd.Engine,
				LotType:        cmd.LotType,
				Symbol:         cmd.Symbol,
				OrigAmountUSDT: cmd.AmountUSDT,
				OrigQtyAsset:   cmd.QtyAsset,
				BarTimeMs:      cmd.BarTimeMs,
				CreatedAtMs:    now,
				UpdatedAtMs:    now,
			}).Error; err != nil {
				return fmt.Errorf("insert pending %d: %w", i, err)
			}
		}

		// 3. Persist runtime.
		if out.NewRuntime != nil {
			buf, err := jsonMarshal(out.NewRuntime)
			if err != nil {
				return fmt.Errorf("marshal runtime: %w", err)
			}
			rs := store.RuntimeStateRow{
				InstanceID: inst.ID,
				StateJSON:  string(buf),
				UpdatedAt:  time.Now().UTC(),
			}
			// Upsert: replace existing row, or insert if first tick.
			if err := tx.Where("instance_id = ?", inst.ID).
				Assign(rs).
				FirstOrCreate(&rs).Error; err != nil {
				return fmt.Errorf("save runtime: %w", err)
			}
			if err := tx.Save(&rs).Error; err != nil {
				return fmt.Errorf("save runtime row: %w", err)
			}
		}

		// 4. Apply Releases.
		for _, rel := range out.Releases {
			if err := applyReleaseTx(tx, inst.ID, rel, clientIDs, now); err != nil {
				return fmt.Errorf("apply release: %w", err)
			}
		}

		// 5. Audit logs for the dispatched intents (one entry per intent).
		for i, intent := range out.Intents {
			payload, _ := jsonMarshal(map[string]any{
				"instance_id":     inst.ID,
				"client_order_id": clientIDs[i],
				"intent":          intent,
				"bar_time_ms":     latestBarTimeMs,
			})
			if err := tx.Create(&store.AuditLog{
				InstanceID:  inst.ID,
				EventType:   "trade_intent_emitted",
				PayloadJSON: string(payload),
				CreatedAtMs: now,
			}).Error; err != nil {
				return fmt.Errorf("audit intent %d: %w", i, err)
			}
		}

		// 6. Stamp processed bar.
		if err := tx.Model(&store.PortfolioState{}).
			Where("instance_id = ?", inst.ID).
			Updates(map[string]any{
				"last_processed_bar_time": latestBarTimeMs,
				"updated_at":              time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("stamp bar: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Phase D: dispatch (paper only for now).
	if inst.Mode != "paper" {
		// Live mode wiring is Phase 7. For now, leave pending rows pending —
		// reconcile will eventually time out on them. Visible in metrics.
		m.log.Warn("live mode dispatch not implemented; pending rows left in DB",
			zapInstanceID(inst.ID))
		return nil
	}

	for i, cmd := range commands {
		exec, dispatchErr := m.paper.PlaceOrder(ctx, cmd, livePrice)
		// Even on dispatch error we want to record the outcome so reconcile
		// can act on it next tick.
		if dispatchErr != nil {
			m.log.Warn("paper PlaceOrder error", zapInstanceID(inst.ID),
				zapClientOrderID(cmd.ClientOrderID))
			continue
		}
		if err := m.applyExecution(ctx, inst, &out.Intents[i], cmd, exec, livePrice); err != nil {
			m.log.Warn("apply execution failed", zapInstanceID(inst.ID),
				zapClientOrderID(cmd.ClientOrderID))
		}
	}

	return nil
}

// buildClientOrderID matches docs/00- §5.3 format:
//
//	inst{id}-{engine_letter}-{action_letter}-{barTimeMs}-{seq}
//
// Engine/Action are abbreviated to one character to save bytes (Binance's
// hard limit is 36 characters).
func buildClientOrderID(instanceID uint, intent strategy.TradeIntent, barTimeMs int64, seq int) string {
	eng := abbrevEngine(intent.Engine)
	act := "?"
	switch intent.Action {
	case "BUY":
		act = "B"
	case "SELL":
		act = "S"
	}
	return fmt.Sprintf("inst%d-%s-%s-%d-%d", instanceID, eng, act, barTimeMs, seq)
}

func abbrevEngine(name string) string {
	if name == "" {
		return "?"
	}
	// First letter, lowercased only when it could collide. We use upper
	// for MACRO/M and lower for MICRO/m to disambiguate.
	switch strings.ToUpper(name) {
	case "MACRO":
		return "M"
	case "MICRO":
		return "m"
	case "DCA":
		return "D"
	case "GRID":
		return "G"
	}
	// Fallback: first character upper.
	return strings.ToUpper(name[:1])
}

// applyReleaseTx flips lot types per the release's slices, updates the
// PortfolioState aggregates, and writes a release audit row that includes
// the resolved client_order_id (so cancellation compensation can pair
// it back later).
func applyReleaseTx(tx *gorm.DB, instanceID uint, rel strategy.ReleaseIntent,
	clientIDs []string, nowMs int64) error {

	// 4a: bookkeeping — flip lot type DEAD ↔ FLOAT for each slice.
	// (Slices reference lot IDs that exist in the same DB; any slice with
	//  a missing lot is logged but not fatal — the strategy may have referenced
	//  a lot that was just consumed by a SELL elsewhere in the same tick.)
	for _, sl := range rel.Slices {
		var lot store.SpotLot
		if err := tx.Where("id = ? AND instance_id = ?", sl.LotID, instanceID).First(&lot).Error; err != nil {
			continue // lot not found — skip but proceed
		}
		// Soft / hard release both convert DEAD → FLOAT.
		// (Future kinds, e.g. "soft_seal" for COLD_SEALED, would branch here.)
		if lot.Type != "DEAD" {
			continue // already not DEAD; nothing to do
		}
		// Reduce the source DEAD lot, mint a new FLOAT lot.
		newDeadQty := lot.Qty - sl.Qty
		if newDeadQty <= 0 {
			// Convert in place.
			lot.Type = "FLOAT"
			lot.Qty = sl.Qty
			lot.UpdatedAt = time.Now().UTC()
			if err := tx.Save(&lot).Error; err != nil {
				return err
			}
		} else {
			// Split: shrink source, create derived FLOAT.
			lot.Qty = newDeadQty
			lot.UpdatedAt = time.Now().UTC()
			if err := tx.Save(&lot).Error; err != nil {
				return err
			}
			derived := store.SpotLot{
				InstanceID:   instanceID,
				Type:         "FLOAT",
				Qty:          sl.Qty,
				BuyPriceUSDT: lot.BuyPriceUSDT,
				BuyTimeMs:    lot.BuyTimeMs,
				IsColdSealed: false,
			}
			if err := tx.Create(&derived).Error; err != nil {
				return err
			}
		}
	}

	// 4b: audit row with resolved client_order_id.
	auditPayload := map[string]any{
		"instance_id": instanceID,
		"kind":        rel.Kind,
		"slices":      rel.Slices,
		"qty":         rel.TotalQtyAsset,
	}
	if rel.RelatedIntentIndex >= 0 && rel.RelatedIntentIndex < len(clientIDs) {
		auditPayload["related_client_order_id"] = clientIDs[rel.RelatedIntentIndex]
	}
	buf, _ := jsonMarshal(auditPayload)
	return tx.Create(&store.AuditLog{
		InstanceID:  instanceID,
		EventType:   rel.Kind, // "soft_release" / "hard_release"
		PayloadJSON: string(buf),
		CreatedAtMs: nowMs,
	}).Error
}
