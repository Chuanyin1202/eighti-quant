// Package instance owns the per-Instance lifecycle inside SaaS:
//   - champion loading (with the version-aware read-through)
//   - promote (advisory-locked, BLOCKED wake-up)
//   - the per-tick decision flow (Phase 6c)
//
// All DB access is GORM through *store.DB; the package never imports specific
// strategy packages — it consumes Strategy via the registry in
// internal/strategy. GA evolution is a separate concern (internal/saas/ga).
package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ErrNoChampion is returned by LoadChampion when there is no active champion
// for the given (strategy, symbol). The Tick caller must transition the
// instance to BLOCKED and skip Step().
var ErrNoChampion = errors.New("instance: no champion available")

// Champion is the in-memory representation handed back from LoadChampion.
//
// The exact ParamPack shape is strategy-defined; consumers cast it via the
// strategy package's DecodeParams. We carry the bytes here rather than the
// decoded struct so LoadChampion stays strategy-agnostic.
type Champion struct {
	GeneRecordID  uint
	StrategyID    string
	Symbol        string
	ParamPackJSON []byte
	ActivatedAt   int64 // unix ms; matches GeneRecord.ActivatedAt
}

// championCacheTTL is the Redis cache TTL. Spec §5.3 says 24h — long enough
// that flapping doesn't matter, short enough that a stale row eventually
// self-corrects even if every Promote DEL fails.
const championCacheTTL = 24 * time.Hour

// championCacheKey builds the Redis key for a (strategy, symbol) pair.
//
// Format mirrors docs §5.3 — "champion:{strategy_id}:{symbol}".
func championCacheKey(strategyID, symbol string) string {
	return "champion:" + strategyID + ":" + symbol
}

// LoadChampion is the version-aware read-through described in
// docs/20-進化計算引擎.md §5.3.
//
// Algorithm:
//  1. Try Redis cache (best-effort).
//  2. ALWAYS perform an atomic DB read: fetch the row WHERE role='champion'
//     in a single query — never two-step (head id + body) because that
//     opens a race window with concurrent Promote (round-11 fix).
//  3. If cache exists AND its ActivatedAt matches the DB row, return cache.
//     Otherwise return DB and write back to cache.
//  4. If the DB has no champion, return ErrNoChampion. The caller must
//     transition the instance to BLOCKED.
func LoadChampion(ctx context.Context, db *store.DB, rds *store.Redis,
	strategyID, symbol string) (*Champion, error) {

	cacheKey := championCacheKey(strategyID, symbol)

	// Step 1: cache attempt (don't fail if Redis is unhappy — it's a cache).
	var cached *Champion
	if rds != nil {
		raw, err := rds.Get(ctx, cacheKey)
		if err == nil {
			if c := decodeChampionCache(raw); c != nil {
				cached = c
			}
		}
		// If err != nil (including ErrCacheMiss), fall through to DB.
	}

	// Step 2: atomic DB read.
	var rec store.GeneRecord
	err := db.WithContext(ctx).
		Where("strategy_id = ? AND symbol = ? AND role = ?", strategyID, symbol, "champion").
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Best-effort cache invalidation — the cache should ALSO not show
		// a champion if the DB doesn't.
		if rds != nil {
			_ = rds.Del(ctx, cacheKey)
		}
		return nil, ErrNoChampion
	}
	if err != nil {
		return nil, fmt.Errorf("instance: load champion: %w", err)
	}

	dbActivated := int64(0)
	if rec.ActivatedAt != nil {
		dbActivated = *rec.ActivatedAt
	}

	// Step 3: validate cache against the fresh DB row.
	if cached != nil &&
		cached.GeneRecordID == rec.ID &&
		cached.ActivatedAt == dbActivated {
		return cached, nil
	}

	// Step 4: DB wins; write back to cache (best-effort).
	c := &Champion{
		GeneRecordID:  rec.ID,
		StrategyID:    rec.StrategyID,
		Symbol:        rec.Symbol,
		ParamPackJSON: []byte(rec.ParamPackJSON),
		ActivatedAt:   dbActivated,
	}
	if rds != nil {
		if buf := encodeChampionCache(c); buf != nil {
			_ = rds.SetEx(ctx, cacheKey, buf, championCacheTTL)
		}
	}
	return c, nil
}

// InvalidateChampionCache deletes the Redis cache entry. Promote calls this
// AFTER the DB transaction commits (cache-aside pattern).
//
// Returns nil even on Redis failure — the version-aware Get path will
// detect the stale entry on the next read and self-heal.
func InvalidateChampionCache(ctx context.Context, rds *store.Redis,
	strategyID, symbol string) error {
	if rds == nil {
		return nil
	}
	if err := rds.Del(ctx, championCacheKey(strategyID, symbol)); err != nil {
		// Swallowing here is intentional — see InvalidateChampionCache docstring.
		return nil
	}
	return nil
}

// championCacheBlob is the on-the-wire shape of a cached Champion. Kept
// separate from the public struct so we can evolve persistence without
// breaking callers.
type championCacheBlob struct {
	GeneRecordID  uint   `json:"gene_record_id"`
	StrategyID    string `json:"strategy_id"`
	Symbol        string `json:"symbol"`
	ParamPackJSON []byte `json:"param_pack_json"`
	ActivatedAt   int64  `json:"activated_at"`
}

func encodeChampionCache(c *Champion) []byte {
	if c == nil {
		return nil
	}
	buf, err := jsonMarshal(championCacheBlob{
		GeneRecordID:  c.GeneRecordID,
		StrategyID:    c.StrategyID,
		Symbol:        c.Symbol,
		ParamPackJSON: c.ParamPackJSON,
		ActivatedAt:   c.ActivatedAt,
	})
	if err != nil {
		return nil
	}
	return buf
}

func decodeChampionCache(raw []byte) *Champion {
	var blob championCacheBlob
	if err := jsonUnmarshal(raw, &blob); err != nil {
		return nil
	}
	return &Champion{
		GeneRecordID:  blob.GeneRecordID,
		StrategyID:    blob.StrategyID,
		Symbol:        blob.Symbol,
		ParamPackJSON: blob.ParamPackJSON,
		ActivatedAt:   blob.ActivatedAt,
	}
}

// silenceRedisImport keeps go.mod cleanly listing the redis dep even before
// any direct call lands here.
var _ = redis.Nil
