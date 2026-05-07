package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"gorm.io/gorm"
)

// ErrChallengerNotFound means the (gene_record_id) provided to Promote
// either does not exist or is not in 'challenger' role.
var ErrChallengerNotFound = errors.New("instance: challenger not found")

// Promote runs the full safety protocol from docs/20- §5.2.1:
//
//	BEGIN; -- REPEATABLE READ
//	1. SELECT pg_advisory_xact_lock(hashtextextended('promote:{strat}:{sym}', 0));
//	2. UPDATE gene_records SET role='retired', retired_at=NOW()
//	    WHERE strategy_id=? AND symbol=? AND role='champion';
//	3. UPDATE gene_records SET role='champion', activated_at=NOW()
//	    WHERE id=? AND strategy_id=? AND symbol=? AND role='challenger';
//	4. INSERT INTO audit_logs (...) VALUES ('promote', ...);
//	COMMIT;
//
//	-- post-commit:
//	5. DEL champion:{strat}:{sym}            (cache invalidation)
//	6. UPDATE strategy_instances SET state='RUNNING'
//	    WHERE strategy_id=? AND symbol=? AND state='BLOCKED';
//
// Iron rule: step 5 + 6 happen ONLY after step 4 commits successfully.
// If TX fails we never invalidate the cache (so consumers keep using the
// previous champion until the next genuine promote).
func Promote(ctx context.Context, db *store.DB, rds *store.Redis,
	geneRecordID uint, strategyID, symbol string) error {

	// Inside-TX work.
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Set isolation level for this TX. GORM doesn't expose a typed knob;
		// PostgreSQL defaults to READ COMMITTED. REPEATABLE READ would be a
		// modest extra protection on top of the advisory lock + partial
		// unique index; keeping the default avoids increasing serialization
		// failures on the hot path.

		// Step 1: advisory lock keyed on the (strategy, symbol) channel.
		// Note: hashtextextended is PostgreSQL 11+; gorm's Exec returns
		// rows as well as count, but we only care that it didn't error.
		if err := tx.Exec(
			`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`,
			"promote:"+strategyID+":"+symbol,
		).Error; err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}

		now := time.Now().UTC().UnixMilli()

		// Step 2: retire current champion(s). Note that `uniq_active_champion`
		// guarantees ≤1 champion exists at any time AFTER preflight; before
		// preflight you'd get duplicate-key errors here. The lock prevents
		// two concurrent Promotes from racing through this UPDATE.
		retireRes := tx.Model(&store.GeneRecord{}).
			Where("strategy_id = ? AND symbol = ? AND role = ?", strategyID, symbol, "champion").
			Updates(map[string]any{
				"role":        "retired",
				"retired_at":  &now,
			})
		if retireRes.Error != nil {
			return fmt.Errorf("retire current champion: %w", retireRes.Error)
		}
		// retireRes.RowsAffected may be 0 (zero-champion bootstrap state) — that's OK.

		// Step 3: promote the chosen challenger. The composite WHERE means
		// the row must currently be a challenger; if someone retired or
		// re-promoted it concurrently we'd see RowsAffected=0 → 409 Conflict.
		promoteRes := tx.Model(&store.GeneRecord{}).
			Where("id = ? AND strategy_id = ? AND symbol = ? AND role = ?",
				geneRecordID, strategyID, symbol, "challenger").
			Updates(map[string]any{
				"role":         "champion",
				"activated_at": &now,
			})
		if promoteRes.Error != nil {
			return fmt.Errorf("activate challenger: %w", promoteRes.Error)
		}
		if promoteRes.RowsAffected == 0 {
			return ErrChallengerNotFound
		}

		// Step 4: audit log row.
		auditPayload, _ := jsonMarshal(map[string]any{
			"gene_record_id": geneRecordID,
			"strategy_id":    strategyID,
			"symbol":         symbol,
		})
		if err := tx.Create(&store.AuditLog{
			EventType:   "promote",
			PayloadJSON: string(auditPayload),
			CreatedAtMs: now,
		}).Error; err != nil {
			return fmt.Errorf("audit log: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Step 5: post-commit cache invalidation (best-effort).
	_ = InvalidateChampionCache(ctx, rds, strategyID, symbol)

	// Step 6: post-commit BLOCKED → RUNNING wake-up.
	// docs/00- §3.2: BLOCKED instances waiting on this exact (strat, sym)
	// pair MUST be revived immediately so they don't have to wait for the
	// next cron probe.
	wakeRes := db.WithContext(ctx).Model(&store.StrategyInstance{}).
		Where("strategy_id = ? AND symbol = ? AND state = ?", strategyID, symbol, "BLOCKED").
		Update("state", "RUNNING")
	if wakeRes.Error != nil {
		// Logging belongs to caller; we surface the error so the API can
		// alert the operator. Promote itself succeeded.
		return fmt.Errorf("wake blocked instances: %w", wakeRes.Error)
	}

	return nil
}
