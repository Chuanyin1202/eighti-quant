package strategy

import (
	"fmt"
	"sync"
)

// Strategy is the minimum contract every EightiQuant strategy implements.
//
// Strategies that need GA evolution must additionally satisfy EvolvableStrategy
// (defined in evolvable.go). The framework's cron tick uses Strategy directly;
// EvolvableStrategy is only consumed by the GA engine.
type Strategy interface {
	Manifest() Manifest
	Step(input StrategyInput, params Params) StrategyOutput
	DefaultParams() Params
}

// Manifest is strategy metadata. The framework reads it to decide:
//   - which symbols this strategy supports
//   - what historical data to load
//   - whether to run a GA epoch for it
//   - how many warmup bars to require before invoking Step()
type Manifest struct {
	ID                  string   // unique, e.g. "lunar-spot-v1"
	Name                string   // user-facing label
	Version             string   // semantic, e.g. "1.0.0"
	IsSpot              bool     // framework currently requires spot=true
	SupportedSymbols    []string // nil means "any symbol"
	RequiredDataKind    string   // "close-only" / "ohlcv" — close-only only for now
	MinWarmupBars       int
	AggregationInterval string // "1h" / "4h" / "1d"
	SupportsEvolution   bool   // if true, must also implement EvolvableStrategy
}

// registry stores strategies registered via Register().
// Strategies register themselves in init() functions inside their own packages.
type registry struct {
	mu  sync.RWMutex
	all map[string]Strategy
}

var defaultRegistry = &registry{all: make(map[string]Strategy)}

// Register adds a strategy to the global registry. Typically called from a
// strategy package's init():
//
//	func init() { strategy.Register(&Strategy{}) }
//
// Panics on duplicate IDs to surface registration bugs at startup.
func Register(s Strategy) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()

	id := s.Manifest().ID
	if id == "" {
		panic("strategy: manifest ID must not be empty")
	}
	if _, exists := defaultRegistry.all[id]; exists {
		panic(fmt.Sprintf("strategy: duplicate registration for ID %q", id))
	}
	defaultRegistry.all[id] = s
}

// Get returns a registered strategy by ID, or (nil, false) if not found.
func Get(id string) (Strategy, bool) {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	s, ok := defaultRegistry.all[id]
	return s, ok
}

// All returns a snapshot of all registered strategies. The returned map is a
// fresh copy and may be mutated by the caller.
func All() map[string]Strategy {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	out := make(map[string]Strategy, len(defaultRegistry.all))
	for k, v := range defaultRegistry.all {
		out[k] = v
	}
	return out
}
