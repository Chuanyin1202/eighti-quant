// Package strategy defines the SDK contract every EightiQuant strategy implements.
//
// All types here are framework-stable: changes break every strategy at once.
// Spec: docs/10-策略框架接口.md.
package strategy

// StrategyInput is the immutable per-tick snapshot fed to Step().
//
// Iron rules (enforced by ripgrep in CI, see CLAUDE.md §6.1):
//   - Step() must be a pure function. No I/O, no clock, no rand.
//   - Decisions must use LatestBarTimeMs, NEVER NowMs (NowMs is for diagnostics only).
//   - The kernel must not branch on Symbol or Mode.
type StrategyInput struct {
	NowMs           int64 // wall-clock ms; diagnostics only
	LatestBarTimeMs int64 // ms; close timestamp of the latest completed bar

	Closes     []float64 // ascending; len ≥ Manifest.MinWarmupBars
	Timestamps []int64   // same length as Closes

	LivePrice float64 // live ticker price (順勢價); for sizing, not for time decisions

	Portfolio PortfolioSnapshot
	Runtime   RuntimeState // strategy-defined, JSON-serializable

	// Symbol metadata: only for sizing/conversion, NOT for branching.
	Symbol      string
	LotStepSize float64
	LotMinQty   float64
	MinOrderUSD float64
}

// PortfolioSnapshot summarizes the instance's current account state.
//
// Aggregate fields (DeadAsset etc.) MUST equal the sum of matching Lots
// (within LotMinQty tolerance). The framework verifies this invariant
// on Agent reconnect; see docs/00-架構總覽.md §5.5.4.
type PortfolioSnapshot struct {
	USDTBalance     float64
	DeadAsset       float64
	FloatAsset      float64
	ColdSealedAsset float64
	Lots            []LotEntry
}

// LotEntry is the per-lot detail backing the aggregate Asset fields.
type LotEntry struct {
	ID           uint
	Type         string // "DEAD" / "FLOAT" / "COLD_SEALED"
	Qty          float64
	BuyPriceUSDT float64
	BuyTimeMs    int64
	IsColdSealed bool
}

// StrategyOutput is the per-tick decision packet returned by Step().
type StrategyOutput struct {
	Intents     []TradeIntent
	Releases    []ReleaseIntent
	NewRuntime  RuntimeState
	Diagnostics map[string]float64 // for UI / debugging only
}

// TradeIntent is a single buy/sell decision. The framework's Phase C
// translates this into a TradeCommand (with client_order_id) and persists
// a pending SpotExecution row before dispatching to the broker.
type TradeIntent struct {
	Action     string  // "BUY" / "SELL"
	Engine     string  // strategy-defined: "MACRO" / "MICRO" / "GRID" / ...
	LotType    string  // "DEAD" / "FLOAT" — where new fills are bucketed
	AmountUSDT float64 // BUY uses this
	QtyAsset   float64 // SELL uses this
}

// ReleaseIntent transfers asset between lot types in the SaaS ledger only.
// Releases are NEVER dispatched to the broker; they are bookkeeping ops.
//
// Hard releases pair with a SELL TradeIntent so cancellation can roll back
// the lot type change atomically. The pairing key is the SELL's index in
// StrategyOutput.Intents (0-based) — Step() cannot know the client_order_id
// because that is generated later in Phase C.
type ReleaseIntent struct {
	Kind          string         // "soft_release" / "hard_release"
	Slices        []ReleaseSlice // FIFO by lot.BuyTimeMs
	TotalQtyAsset float64        // = sum(Slices[i].Qty)

	// hard_release: the SELL TradeIntent's index in StrategyOutput.Intents.
	// soft_release: -1.
	RelatedIntentIndex int
}

// ReleaseSlice is the per-lot release amount; partial slices supported.
type ReleaseSlice struct {
	LotID uint
	Qty   float64
}

// RuntimeState is strategy-defined opaque state, persisted as JSON between ticks.
// Strategies should keep it small and easy to migrate (avoid embedding maps with
// dynamic keys when possible).
type RuntimeState = any

// Params is strategy-defined opaque parameter struct, supplied by either
// DefaultParams() (non-evolvable) or the GA champion (evolvable).
type Params = any
