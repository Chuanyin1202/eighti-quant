-- EightiQuant non-structural DB objects (partial indexes, check constraints).
--
-- Every DDL here MUST:
--   1. Use IF NOT EXISTS so it can run on every startup idempotently.
--   2. Have a corresponding doc reference in docs/ explaining its purpose.
--
-- Executed by store.ApplyIndexes() after AutoMigrate.

-- uniq_active_champion: enforces "at most one champion per (strategy_id, symbol)".
-- Doc: docs/20-進化計算引擎.md §5.2.2.
-- Rationale: GORM struct tag cannot express a partial index; the application
-- layer's advisory lock guards concurrent promote, but this index is the
-- last-line DB-level invariant against split-brain champions.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_active_champion
    ON gene_records (strategy_id, symbol)
    WHERE role = 'champion';
