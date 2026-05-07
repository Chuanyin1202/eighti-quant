// Command saas is the EightiQuant SaaS-side daemon (decision brain).
//
// Phase 2 wires up: Config / DB / Redis / Auth / strategy registry.
// Phase 3+ adds: cron tick / WS hub / GA engine / REST API.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/broker/paper"
	"github.com/Chuanyin1202/eighti-quant/internal/broker/priceprovider"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/auth"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/config"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/cron"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/instance"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
	"go.uber.org/zap"

	// Strategy reference implementations register themselves via init().
	// Adding/removing a strategy is a one-line edit here.
	_ "github.com/Chuanyin1202/eighti-quant/internal/strategies/grid"
	_ "github.com/Chuanyin1202/eighti-quant/internal/strategies/lunarspotv1"
	_ "github.com/Chuanyin1202/eighti-quant/internal/strategies/simpledca"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	logger := mustLogger()
	defer func() { _ = logger.Sync() }()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Fatal("config load failed", zap.Error(err))
	}
	logger.Info("config loaded",
		zap.String("app_role", cfg.AppRole),
		zap.String("server_addr", cfg.Server.Addr),
		zap.String("db_host", cfg.Database.Host),
		zap.String("db_name", cfg.Database.Name),
	)

	db, err := store.NewDB(cfg.Database)
	if err != nil {
		logger.Fatal("db init failed", zap.Error(err))
	}
	logger.Info("postgres ready (automigrate + preflight + indexes.sql applied)")
	_ = db

	redis, err := store.NewRedis(cfg.Redis)
	if err != nil {
		logger.Fatal("redis init failed", zap.Error(err))
	}
	defer func() { _ = redis.Close() }()
	logger.Info("redis ready", zap.String("addr", cfg.Redis.Addr))

	authSvc := auth.New(cfg.JWT)
	_ = authSvc
	logger.Info("auth ready", zap.Int("ttl_hours", cfg.JWT.TTLHours))

	strategies := strategy.All()
	logger.Info("strategy registry initialized", zap.Int("count", len(strategies)))
	for id, s := range strategies {
		m := s.Manifest()
		logger.Info("registered strategy",
			zap.String("id", id),
			zap.String("name", m.Name),
			zap.String("version", m.Version),
			zap.Bool("supports_evolution", m.SupportsEvolution),
		)
	}

	// Phase 6 wiring: PriceProvider + PaperBroker + Manager + Scheduler.
	prices := priceprovider.NewBinancePublic(10 * time.Second)
	paperBroker := paper.NewSimple(0.001, 10.1, nil)
	mgr := instance.NewManager(db, redis, prices, paperBroker, logger)
	scheduler := cron.New(db, mgr, logger)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := scheduler.Start(rootCtx); err != nil {
		logger.Fatal("scheduler start failed", zap.Error(err))
	}
	logger.Info("cron scheduler started", zap.String("interval", "every minute"))

	// Phase 7+ TODO: WS hub for live mode, REST API, graceful shutdown drain.
	logger.Info("EightiQuant SaaS bootstrap complete; paper-mode tick loop active")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("shutdown signal received; draining cron")
	scheduler.AwaitDrain(30 * time.Second)
	logger.Info("goodbye")
}

func mustLogger() *zap.Logger {
	l, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("zap init: %v", err)
	}
	return l
}
