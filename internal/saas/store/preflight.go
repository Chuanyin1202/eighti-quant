package store

import (
	"fmt"

	"gorm.io/gorm"
)

// championIndexPreflight detects pre-existing duplicate champions before the
// uniq_active_champion partial index is installed.
//
// Why this is needed: `CREATE UNIQUE INDEX IF NOT EXISTS` skips re-creation
// but does NOT silently allow duplicate data. If gene_records already has
// >1 champion for a (strategy_id, symbol) at install time, the CREATE will
// fail and SaaS will refuse to start.
//
// This preflight surfaces the issue with a clear error message instead of
// the generic "could not create unique index" from PostgreSQL.
//
// Manual cleanup procedure: docs/20-進化計算引擎.md §5.2.4.
func championIndexPreflight(gdb *gorm.DB) error {
	type dup struct {
		StrategyID string
		Symbol     string
		Count      int64
	}
	var dupes []dup
	err := gdb.Raw(`
		SELECT strategy_id, symbol, COUNT(*) AS count
		  FROM gene_records
		 WHERE role = 'champion'
		 GROUP BY strategy_id, symbol
		HAVING COUNT(*) > 1
	`).Scan(&dupes).Error
	if err != nil {
		return fmt.Errorf("preflight query: %w", err)
	}

	if len(dupes) == 0 {
		return nil
	}

	var msg string
	for _, d := range dupes {
		msg += fmt.Sprintf("  - (strategy_id=%q, symbol=%q): %d champions\n",
			d.StrategyID, d.Symbol, d.Count)
	}
	return fmt.Errorf(
		"duplicate champions detected (manual cleanup required, see docs/20-進化計算引擎.md §5.2.4):\n%s",
		msg,
	)
}
