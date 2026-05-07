package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/broker"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// applyExecution turns a paper-broker fill into ledger updates: marks the
// SpotExecution filled, writes a TradeRecord, and applies the lot/balance
// delta for BUY or SELL.
//
// Single transaction: pending row update + TradeRecord insert + balance/lot
// updates land together; if any fails the whole apply rolls back.
func (m *Manager) applyExecution(ctx context.Context, inst *store.StrategyInstance,
	intent *strategy.TradeIntent, cmd broker.TradeCommand, exec broker.Execution,
	livePrice float64) error {

	now := time.Now().UTC().UnixMilli()

	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Update pending → final status.
		updates := map[string]any{
			"status":            exec.Status,
			"exchange_order_id": exec.OrderID,
			"updated_at_ms":     now,
		}
		if exec.Status == broker.StatusFilled || exec.Status == broker.StatusPartiallyFilled {
			updates["filled_qty"] = exec.FilledQty
			updates["filled_price_usdt"] = exec.FilledPriceUSDT
			updates["fee_amount"] = exec.FeeAmount
			updates["fee_asset"] = exec.FeeAsset
		}
		if err := tx.Model(&store.SpotExecution{}).
			Where("client_order_id = ?", cmd.ClientOrderID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update pending: %w", err)
		}

		// Rejected / canceled: nothing to apply on the ledger side.
		if exec.Status != broker.StatusFilled && exec.Status != broker.StatusPartiallyFilled {
			return nil
		}

		// 2. TradeRecord row.
		if err := tx.Create(&store.TradeRecord{
			InstanceID:      inst.ID,
			ClientOrderID:   cmd.ClientOrderID,
			Action:          cmd.Action,
			Engine:          cmd.Engine,
			Symbol:          cmd.Symbol,
			FilledQty:       exec.FilledQty,
			FilledPriceUSDT: exec.FilledPriceUSDT,
			FeeAmount:       exec.FeeAmount,
			FeeAsset:        exec.FeeAsset,
			ExecutedAtMs:    exec.ExecutedAtMs,
		}).Error; err != nil {
			return fmt.Errorf("trade record: %w", err)
		}

		// 3. Apply ledger update.
		switch cmd.Action {
		case broker.ActionBUY:
			return applyBuyTx(tx, inst.ID, cmd, exec, intent, now)
		case broker.ActionSELL:
			return applySellTx(tx, inst.ID, cmd, exec, now)
		default:
			return fmt.Errorf("unknown action %q", cmd.Action)
		}
	})
}

// applyBuyTx debits USDT, creates a new SpotLot of the requested type, and
// updates the aggregate fields in PortfolioState.
func applyBuyTx(tx *gorm.DB, instanceID uint, cmd broker.TradeCommand,
	exec broker.Execution, intent *strategy.TradeIntent, nowMs int64) error {

	if exec.FilledQty <= 0 {
		return nil
	}

	// Update PortfolioState atomically. We compute the new aggregate by
	// reading the row inside the TX (we're already in REPEATABLE READ via
	// the outer Transaction; PostgreSQL default READ COMMITTED is fine
	// here because the cron tick is the only writer per instance).
	var port store.PortfolioState
	if err := tx.Where("instance_id = ?", instanceID).First(&port).Error; err != nil {
		return fmt.Errorf("load portfolio: %w", err)
	}
	port.USDTBalance -= cmd.AmountUSDT
	switch intent.LotType {
	case "DEAD":
		port.DeadAsset += exec.FilledQty
	case "FLOAT":
		port.FloatAsset += exec.FilledQty
	default:
		return fmt.Errorf("unsupported buy LotType %q", intent.LotType)
	}
	port.UpdatedAt = time.Now().UTC()
	if err := tx.Save(&port).Error; err != nil {
		return fmt.Errorf("save portfolio: %w", err)
	}

	// Create the new lot.
	if err := tx.Create(&store.SpotLot{
		InstanceID:   instanceID,
		Type:         intent.LotType,
		Qty:          exec.FilledQty,
		BuyPriceUSDT: exec.FilledPriceUSDT,
		BuyTimeMs:    cmd.BarTimeMs,
		IsColdSealed: false,
	}).Error; err != nil {
		return fmt.Errorf("create lot: %w", err)
	}

	_ = nowMs
	return nil
}

// applySellTx credits USDT (gross - fee already netted into exec) and
// reduces FLOAT lots FIFO-style by exec.FilledQty.
func applySellTx(tx *gorm.DB, instanceID uint, cmd broker.TradeCommand,
	exec broker.Execution, nowMs int64) error {

	if exec.FilledQty <= 0 {
		return nil
	}

	// Update PortfolioState.
	var port store.PortfolioState
	if err := tx.Where("instance_id = ?", instanceID).First(&port).Error; err != nil {
		return fmt.Errorf("load portfolio: %w", err)
	}
	port.FloatAsset -= exec.FilledQty
	if port.FloatAsset < 0 {
		// Should not happen if oversell guards work — log via audit but
		// don't fail the TX (better to surface than to roll back a real fill).
		port.FloatAsset = 0
	}
	gross := exec.FilledQty * exec.FilledPriceUSDT
	port.USDTBalance += gross - exec.FeeAmount
	port.UpdatedAt = time.Now().UTC()
	if err := tx.Save(&port).Error; err != nil {
		return fmt.Errorf("save portfolio: %w", err)
	}

	// Reduce FLOAT lots FIFO.
	var lots []store.SpotLot
	if err := tx.Where("instance_id = ? AND type = ? AND is_cold_sealed = ?",
		instanceID, "FLOAT", false).Order("buy_time_ms ASC").Find(&lots).Error; err != nil {
		return fmt.Errorf("load lots for sell: %w", err)
	}
	remaining := exec.FilledQty
	for _, l := range lots {
		if remaining <= 0 {
			break
		}
		if l.Qty <= remaining {
			remaining -= l.Qty
			if err := tx.Delete(&l).Error; err != nil {
				return fmt.Errorf("delete consumed lot: %w", err)
			}
			continue
		}
		l.Qty -= remaining
		l.UpdatedAt = time.Now().UTC()
		if err := tx.Save(&l).Error; err != nil {
			return fmt.Errorf("update partial lot: %w", err)
		}
		remaining = 0
	}
	_ = nowMs
	return nil
}

// zapInstanceID / zapClientOrderID are tiny helpers so call sites stay readable.
func zapInstanceID(id uint) zap.Field      { return zap.Uint("instance_id", id) }
func zapClientOrderID(cid string) zap.Field { return zap.String("client_order_id", cid) }
