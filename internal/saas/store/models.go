// Package store defines all persistence concerns: GORM models, AutoMigrate,
// Redis client, and indexes.sql execution.
//
// Iron rule (CLAUDE.md §2.4):
//   - Table structure is the single source of truth in these structs.
//   - Use GORM AutoMigrate for tables / columns / standard indexes.
//   - Partial indexes & check constraints live in indexes.sql, executed
//     after AutoMigrate by db.go.
package store

import "time"

// User is the end-user account that owns instances.
type User struct {
	ID               uint      `gorm:"primaryKey"`
	Email            string    `gorm:"uniqueIndex;size:255;not null"`
	PasswordHash     string    `gorm:"size:255;not null"`
	SubscriptionTier string    `gorm:"size:32;default:'free'"` // "free" / "pro" / "enterprise"
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// StrategyEntry is the registered strategy template (one row per strategy ID).
// Manifest fields are mirrored from internal/strategy.Strategy.Manifest() at
// startup; this lets the API answer "list available strategies" without
// reflection at request time.
type StrategyEntry struct {
	ID                  uint   `gorm:"primaryKey"`
	StrategyID          string `gorm:"uniqueIndex;size:64;not null"` // e.g. "lunar-spot-v1"
	Name                string `gorm:"size:128;not null"`
	Version             string `gorm:"size:32"`
	IsSpot              bool
	SupportedSymbols    string // JSON-encoded []string; empty = "any"
	RequiredDataKind    string `gorm:"size:32"`
	MinWarmupBars       int
	AggregationInterval string `gorm:"size:16"`
	SupportsEvolution   bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// StrategyInstance is a user's running configuration of a strategy.
//
// State machine: see docs/00-架構總覽.md §3.2.
//   RUNNING / STOPPED / BLOCKED / ERROR
//
// Mode: "paper" / "live"
type StrategyInstance struct {
	ID         uint   `gorm:"primaryKey"`
	UserID     uint   `gorm:"index;not null"`
	StrategyID string `gorm:"index;size:64;not null"`
	Symbol     string `gorm:"index;size:32;not null"`
	State      string `gorm:"index;size:16;not null"` // RUNNING/STOPPED/BLOCKED/ERROR
	Mode       string `gorm:"size:8;not null"`        // paper/live

	// SpawnPoint snapshot at instance creation (also stored as Champion's
	// SpawnPoint when GA runs). JSON-encoded.
	SpawnPointJSON string `gorm:"type:text"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// PortfolioState is the per-instance latest aggregate balance snapshot.
// Lots are sourced from SpotLot table; aggregates here are denormalized for
// O(1) read. Reconcile checks SUM(SpotLot) == this snapshot (within tolerance).
type PortfolioState struct {
	InstanceID            uint    `gorm:"primaryKey"`
	USDTBalance           float64
	DeadAsset             float64
	FloatAsset            float64
	ColdSealedAsset       float64
	LastProcessedBarTime  int64
	UpdatedAt             time.Time
}

// RuntimeStateRow holds the strategy-defined opaque runtime state JSON,
// updated atomically with PortfolioState in cron tick Phase C.
type RuntimeStateRow struct {
	InstanceID uint   `gorm:"primaryKey"`
	StateJSON  string `gorm:"type:text"`
	UpdatedAt  time.Time
}

// SpotLot is a per-lot record. Aggregate Asset fields in PortfolioState
// MUST equal SUM(SpotLot.Qty) grouped by Type within tolerance.
type SpotLot struct {
	ID           uint    `gorm:"primaryKey"`
	InstanceID   uint    `gorm:"index;not null"`
	Type         string  `gorm:"size:16;not null"` // "DEAD" / "FLOAT" / "COLD_SEALED"
	Qty          float64
	BuyPriceUSDT float64
	BuyTimeMs    int64   `gorm:"index"`
	IsColdSealed bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SpotExecution is the lifecycle row for a TradeCommand.
//
// Status state machine: pending → filled / canceled.
//   pending: command sent, no terminal state from exchange yet
//   filled:  fully executed (or partially executed then canceled with ExecutedQty>0)
//   canceled: terminal cancel/expire/reject with no fill
type SpotExecution struct {
	ID                   uint   `gorm:"primaryKey"`
	InstanceID           uint   `gorm:"index;not null"`
	ClientOrderID        string `gorm:"uniqueIndex;size:96;not null"`
	ExchangeOrderID      string `gorm:"index;size:64"` // populated after fill
	Status               string `gorm:"index;size:16;not null"` // pending/filled/canceled
	Action               string `gorm:"size:8;not null"`        // BUY/SELL
	Engine               string `gorm:"size:16;not null"`       // strategy-defined
	LotType              string `gorm:"size:16"`                // DEAD/FLOAT
	Symbol               string `gorm:"size:32;not null"`
	OrigAmountUSDT       float64
	OrigQtyAsset         float64
	FilledQty            float64
	FilledPriceUSDT      float64
	FeeAmount            float64
	FeeAsset             string `gorm:"size:16"`
	BarTimeMs            int64
	CreatedAtMs          int64
	UpdatedAtMs          int64
}

// TradeRecord is the post-fill audit-friendly view of executed trades.
type TradeRecord struct {
	ID              uint   `gorm:"primaryKey"`
	InstanceID      uint   `gorm:"index;not null"`
	ClientOrderID   string `gorm:"uniqueIndex;size:96;not null"`
	Action          string `gorm:"size:8;not null"`
	Engine          string `gorm:"size:16"`
	Symbol          string `gorm:"size:32;not null"`
	FilledQty       float64
	FilledPriceUSDT float64
	FeeAmount       float64
	FeeAsset        string `gorm:"size:16"`
	ExecutedAtMs    int64  `gorm:"index"`
}

// AuditLog is the append-only event log for compliance / debugging.
type AuditLog struct {
	ID          uint   `gorm:"primaryKey"`
	InstanceID  uint   `gorm:"index"`
	EventType   string `gorm:"index;size:64;not null"` // e.g. "promote", "soft_release"
	PayloadJSON string `gorm:"type:text"`
	CreatedAtMs int64  `gorm:"index;not null"`
}

// GeneRecord is the GA basin: every chromosome ever produced (and surviving),
// either challenger / champion / retired. The unique active-champion invariant
// is enforced by uniq_active_champion partial index in indexes.sql.
type GeneRecord struct {
	ID            uint   `gorm:"primaryKey"`
	StrategyID    string `gorm:"index:idx_strat_sym;size:64;not null"`
	Symbol        string `gorm:"index:idx_strat_sym;size:32;not null"`
	Role          string `gorm:"size:16;not null"` // challenger / champion / retired
	ParamPackJSON string `gorm:"type:text;not null"`
	ScoreTotal    float64
	MaxDrawdown   float64
	WindowScores  string `gorm:"type:text"` // JSON {"6m":..., "2y":..., ...}
	ActivatedAt   *int64 // null until promoted
	RetiredAt     *int64 // null until retired
	CreatedAt     time.Time
}

// EvolutionTask tracks GA epoch lifecycle.
type EvolutionTask struct {
	ID             uint   `gorm:"primaryKey"`
	StrategyID     string `gorm:"index:idx_evo_strat_sym;size:64;not null"`
	Symbol         string `gorm:"index:idx_evo_strat_sym;size:32;not null"`
	Status         string `gorm:"index;size:16;not null"` // queued/running/completed/failed
	ProgressPct    int
	ConfigJSON     string `gorm:"type:text"`
	BestScoreSoFar float64
	StartedAt      *time.Time
	CompletedAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// MarketDataPoint is the close-only price series. The framework currently
// supports close-only strategies; an OHLCV variant can be added as a parallel
// table when needed without breaking this schema.
type MarketDataPoint struct {
	ID       uint    `gorm:"primaryKey"`
	Symbol   string  `gorm:"size:32;not null;uniqueIndex:idx_sym_int_open"`
	Interval string  `gorm:"size:8;not null;uniqueIndex:idx_sym_int_open"`
	OpenTime int64   `gorm:"not null;uniqueIndex:idx_sym_int_open"` // ms, UTC
	Close    float64
}

// PaperBalance is the in-process simulator ledger for paper instances.
// Kept separate from the live broker's snapshot to avoid mixing virtual
// and real balances.
type PaperBalance struct {
	ID          uint    `gorm:"primaryKey"`
	InstanceID  uint    `gorm:"uniqueIndex:idx_paper_inst_asset;not null"`
	Asset       string  `gorm:"uniqueIndex:idx_paper_inst_asset;size:16;not null"` // "USDT" / "BTC"
	Free        float64
	Locked      float64
	UpdatedAtMs int64
}

// AllModels is the canonical list passed to AutoMigrate. Adding a new model
// here is the only place needed; db.go reads from this list.
func AllModels() []any {
	return []any{
		&User{},
		&StrategyEntry{},
		&StrategyInstance{},
		&PortfolioState{},
		&RuntimeStateRow{},
		&SpotLot{},
		&SpotExecution{},
		&TradeRecord{},
		&AuditLog{},
		&GeneRecord{},
		&EvolutionTask{},
		&MarketDataPoint{},
		&PaperBalance{},
	}
}
