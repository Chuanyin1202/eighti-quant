// Package cron drives the per-minute scan that triggers Manager.Tick on
// active instances.
//
// Scope: scan logic + minute-level scheduling. The actual tick logic lives
// in internal/saas/instance.Manager. This separation keeps the scheduler
// dependency-light (only DB access for the instance list).
package cron

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/instance"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	robfigcron "github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// Scheduler runs a cron entry every minute, scanning the StrategyInstance
// table for RUNNING + BLOCKED rows and dispatching Manager.Tick on each.
//
// Concurrency model: each Tick runs in its own goroutine, with a small
// per-instance mutex map preventing two simultaneous ticks on the same row.
// (Per-instance: at 4h cadence + 60s scan, a single instance triggers at
// most once every 4 hours — but the lock guards us against pathological
// scheduler overlap.)
type Scheduler struct {
	db      *store.DB
	manager *instance.Manager
	log     *zap.Logger

	c        *robfigcron.Cron
	mu       sync.Mutex
	tickLock map[uint]*sync.Mutex
}

// New constructs the Scheduler. Call Start to begin scanning; Stop to halt.
func New(db *store.DB, manager *instance.Manager, log *zap.Logger) *Scheduler {
	if log == nil {
		log = zap.NewNop()
	}
	return &Scheduler{
		db:       db,
		manager:  manager,
		log:      log,
		c:        robfigcron.New(robfigcron.WithSeconds()),
		tickLock: map[uint]*sync.Mutex{},
	}
}

// Start kicks off the per-minute scan. Returns the cron entry ID so callers
// can cancel a specific job if they want, though Stop is the usual exit path.
func (s *Scheduler) Start(ctx context.Context) error {
	// "Every minute on the 0th second."
	_, err := s.c.AddFunc("0 * * * * *", func() {
		s.scanAndTick(ctx)
	})
	if err != nil {
		return err
	}
	s.c.Start()
	return nil
}

// Stop halts the cron entry. Safe to call multiple times.
func (s *Scheduler) Stop() context.Context {
	return s.c.Stop()
}

// scanAndTick fetches all RUNNING + BLOCKED instances and dispatches Tick
// on each. RUNNING follows the full cron-tick flow; BLOCKED runs the
// lightweight champion probe so it can transition back to RUNNING when a
// promote arrives (the active wake-up handles the common case; this is
// the belt-and-suspenders fallback per docs/00- §3.2).
func (s *Scheduler) scanAndTick(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}

	var ids []uint
	err := s.db.WithContext(ctx).Model(&store.StrategyInstance{}).
		Where("state IN ?", []string{"RUNNING", "BLOCKED"}).
		Pluck("id", &ids).Error
	if err != nil {
		s.log.Error("scheduler: load active instances", zap.Error(err))
		return
	}

	for _, id := range ids {
		go s.tickOne(ctx, id)
	}
}

// tickOne acquires the per-instance lock and invokes Manager.Tick.
//
// The lock is a strict single-flight gate: if a previous Tick is still
// running when the next minute fires we silently skip the new attempt.
// At 4h instance cadence with sub-second Tick latency this never matters;
// the guard exists for paranoid pathological cases.
func (s *Scheduler) tickOne(ctx context.Context, instanceID uint) {
	mu := s.lockFor(instanceID)
	if !mu.TryLock() {
		return
	}
	defer mu.Unlock()

	if err := s.manager.Tick(ctx, instanceID); err != nil {
		// Don't escalate to FATAL — one bad instance shouldn't kill the
		// whole scan loop. Log and move on; persistent issues will surface
		// via the audit log.
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("tick failed", zap.Uint("instance_id", instanceID), zap.Error(err))
		}
	}
}

func (s *Scheduler) lockFor(instanceID uint) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	mu, ok := s.tickLock[instanceID]
	if !ok {
		mu = &sync.Mutex{}
		s.tickLock[instanceID] = mu
	}
	return mu
}

// RunOnce is a manual scan trigger — useful for tests and admin endpoints
// (e.g. "force a tick now without waiting for the next minute"). Honors the
// per-instance lock just like the cron-driven path.
func (s *Scheduler) RunOnce(ctx context.Context) {
	s.scanAndTick(ctx)
}

// AwaitDrain returns a channel that closes when all currently-running
// goroutines from Stop()'s pending tasks have finished. Wraps the
// cron.Stop ctx for ergonomic shutdown.
func (s *Scheduler) AwaitDrain(timeout time.Duration) bool {
	doneCtx := s.c.Stop()
	select {
	case <-doneCtx.Done():
		return true
	case <-time.After(timeout):
		return false
	}
}
