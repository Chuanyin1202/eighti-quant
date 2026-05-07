// Command saas is the EightiQuant SaaS-side daemon (decision brain).
//
// Phase 2 wires up: Config / DB / Redis / Auth / strategy registry.
// Phase 3+ adds: cron tick / WS hub / GA engine / REST API.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/auth"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/config"
	"github.com/Chuanyin1202/eighti-quant/internal/saas/store"
	"github.com/Chuanyin1202/eighti-quant/internal/strategy"
	"go.uber.org/zap"

	// Strategy reference implementations register themselves via init().
	// Adding/removing a strategy is a one-line edit here.
	_ "github.com/Chuanyin1202/eighti-quant/internal/strategies/grid"
	_ "github.com/Chuanyin1202/eighti-quant/internal/strategies/simpledca"
	// _ "github.com/Chuanyin1202/eighti-quant/internal/strategies/lunarspotv1" // Phase 4c
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

	// Phase 3+ TODO:
	//   - Init WS hub, instance manager, cron scheduler, REST API.
	//   - Replace this signal-wait with a graceful shutdown sequence
	//     (drain ticks, persist runtime, close WS, close DB).
	logger.Info("EightiQuant SaaS bootstrap complete; awaiting Phase 3+")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("shutdown signal received; goodbye")
}

func mustLogger() *zap.Logger {
	l, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("zap init: %v", err)
	}
	return l
}
