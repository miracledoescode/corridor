// corridord is the one Corridor binary: ingestion supervisor + API.
// Phase 1 wires ingest and /healthz; spread and notify arrive in Phase 3.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miracledoescode/corridor/internal/api"
	"github.com/miracledoescode/corridor/internal/cache"
	"github.com/miracledoescode/corridor/internal/ingest"
	"github.com/miracledoescode/corridor/internal/ingest/kalshi"
	"github.com/miracledoescode/corridor/internal/ingest/polymarket"
	"github.com/miracledoescode/corridor/internal/store"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("bad config", "err", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// WHY wait for the DB instead of crashing: docker's restart policy
	// would also recover us, but a calm retry loop keeps boot logs clean
	// and survives DB restarts mid-deploy without counting as a crash.
	st := mustStore(ctx, cfg, log)
	defer st.Close()

	if cfg.redisURL != "" {
		tb, err := cache.New(cfg.redisURL, log)
		if err != nil {
			// Redis is optional by design — ingestion runs without it.
			log.Warn("redis unavailable, running without top-of-book cache", "err", err)
		} else {
			st.TopBook = tb
			defer tb.Close()
		}
	}

	ua := ingest.UserAgent(cfg.userAgentContact)
	venues := []ingest.VenueSpec{
		{
			Adapter:    polymarket.New(cfg.polymarketGammaURL, cfg.polymarketClobURL, ua, log),
			MetaEvery:  cfg.polymarketMetaEvery,
			QuoteEvery: cfg.quoteEvery,
		},
		{
			Adapter:    kalshi.New(cfg.kalshiBaseURL, ua, log),
			MetaEvery:  cfg.kalshiMetaEvery,
			QuoteEvery: cfg.quoteEvery,
		},
	}
	sup := ingest.NewSupervisor(st, log, venues)

	srv := api.NewServer(":"+cfg.port, st, sup, log)
	go func() {
		log.Info("api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api server failed", "err", err)
		}
	}()

	log.Info("supervisor starting", "venues", len(venues))
	sup.Run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("corridord stopped")
}

func mustStore(ctx context.Context, cfg config, log *slog.Logger) *store.Store {
	for attempt := 0; ; attempt++ {
		st, err := store.New(ctx, cfg.dbURL, log)
		if err == nil {
			if err := store.Migrate(cfg.dbURL); err != nil {
				log.Error("migrations failed", "err", err)
				st.Close()
				os.Exit(1) // a bad migration needs a human, not a retry loop
			}
			return st
		}
		wait := ingest.Backoff(attempt, time.Second, 30*time.Second)
		log.Warn("db not ready, retrying", "err", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			log.Info("shutdown requested before db came up")
			os.Exit(0)
		case <-time.After(wait):
		}
	}
}
