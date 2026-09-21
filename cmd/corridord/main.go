// corridord is the one Corridor binary: ingestion supervisor + spread
// engine + API. Notify (Telegram) is the remaining Phase 3 piece.
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
	"github.com/miracledoescode/corridor/internal/spread"
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

	// Seed the write-time dedup cache from the DB before polling starts, so a
	// restart doesn't re-write a row for every outcome on the first cycle.
	// Best-effort: a failure only costs that one redundant cycle.
	if err := st.WarmDedupCache(ctx); err != nil {
		log.Warn("dedup cache warm failed; first cycle may write redundant rows", "err", err)
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

	// Retention: prune quotes older than the cutoff so the table stays under
	// the Supabase free-tier storage cap. WHY here and not a separate
	// service: corridord is the modular monolith; a once-daily DELETE is a
	// goroutine, not an ops dependency. WHY retain at all (vs keep forever):
	// today Corridor is a comparison tool, not a tick archive. When deep
	// odds history becomes the product (data API), revisit retention + a Pro
	// storage tier.
	go runRetention(ctx, st, cfg.quoteRetentionDays, log)

	// The spread engine is a third supervised concern, isolated from ingestion
	// the same way retention is: its own goroutine, its own failures, logged
	// and never fatal. It reads matched markets and writes alerts; it never
	// touches the venue loops, so a slow or failing scan cannot stall quote
	// capture.
	//
	// WHY opt-in rather than on by default: it only produces trustworthy
	// numbers once venues.fee_model holds verified coefficients. Defaulting it
	// off means a deploy cannot start emitting arb alerts computed from
	// unverified fees.
	if cfg.spreadEvery > 0 {
		eng := spread.NewEngine(st, st, log)
		go eng.Run(ctx, cfg.spreadEvery)
		log.Info("spread engine starting", "interval", cfg.spreadEvery.String())
	} else {
		log.Info("spread engine disabled; set SPREAD_SCAN_INTERVAL_S to enable")
	}

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

// runRetention deletes quotes older than days, once on startup and then
// every 24h, until ctx is cancelled. Failures are logged, never fatal —
// ingestion must not stop because a prune failed (prime directive).
func runRetention(ctx context.Context, st *store.Store, days int, log *slog.Logger) {
	prune := func() {
		n, err := st.DeleteQuotesOlderThan(ctx, days)
		if err != nil {
			log.Error("quote retention failed", "err", err, "retention_days", days)
			return
		}
		log.Info("quote retention", "deleted", n, "retention_days", days)
	}

	prune() // once at startup
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
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
