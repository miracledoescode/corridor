// Package store owns all Postgres access: pool init, embedded goose
// migrations, and the ingest.Sink implementation on top of
// sqlc-generated queries.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"

	"github.com/miracledoescode/corridor/internal/cache"
	"github.com/miracledoescode/corridor/internal/ingest"
	"github.com/miracledoescode/corridor/internal/store/gen"
	"github.com/miracledoescode/corridor/migrations"
)

type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	log  *slog.Logger

	// TopBook is optional: when set, fresh quotes are mirrored to Redis
	// best-effort. A nil or failing cache never blocks the ingest path.
	TopBook *cache.TopBook

	mu       sync.Mutex
	venueIDs map[string]int16 // slug → venues.id; immutable rows, safe to cache
}

func New(ctx context.Context, dbURL string, log *slog.Logger) (*Store, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return &Store{
		pool:     pool,
		q:        gen.New(pool),
		log:      log,
		venueIDs: make(map[string]int16),
	}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate runs the embedded goose migrations. Goose tracks applied
// versions, so calling this on every boot is idempotent.
func Migrate(dbURL string) error {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func (s *Store) venueID(ctx context.Context, slug string) (int16, error) {
	s.mu.Lock()
	if id, ok := s.venueIDs[slug]; ok {
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()

	id, err := s.q.GetVenueIDBySlug(ctx, slug)
	if err != nil {
		return 0, fmt.Errorf("venue %q not seeded: %w", slug, err)
	}
	s.mu.Lock()
	s.venueIDs[slug] = id
	s.mu.Unlock()
	return id, nil
}

// UpsertMarkets implements ingest.Sink. Each market and its outcomes are
// written in one transaction so a half-written market (market row without
// outcomes) can never be observed.
func (s *Store) UpsertMarkets(ctx context.Context, venueSlug string, markets []ingest.Market) (int, error) {
	venueID, err := s.venueID(ctx, venueSlug)
	if err != nil {
		return 0, err
	}

	written := 0
	for _, m := range markets {
		if err := s.upsertOne(ctx, venueID, m); err != nil {
			// WHY continue instead of abort: one malformed market must not
			// block the other 999 from landing — partial ingest beats no
			// ingest (prime directive).
			s.log.Warn("market upsert failed",
				"venue", venueSlug, "venue_market_id", m.VenueMarketID, "err", err)
			continue
		}
		written++
	}
	if written == 0 && len(markets) > 0 {
		return 0, fmt.Errorf("all %d market upserts failed for %s", len(markets), venueSlug)
	}
	return written, nil
}

func (s *Store) upsertOne(ctx context.Context, venueID int16, m ingest.Market) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := s.q.WithTx(tx)
	closeTime := pgtype.Timestamptz{}
	if m.CloseTime != nil {
		closeTime = pgtype.Timestamptz{Time: *m.CloseTime, Valid: true}
	}
	marketID, err := q.UpsertMarket(ctx, gen.UpsertMarketParams{
		VenueID:            venueID,
		VenueMarketID:      m.VenueMarketID,
		Title:              m.Title,
		ResolutionCriteria: pgtype.Text{String: m.ResolutionCriteria, Valid: m.ResolutionCriteria != ""},
		CloseTime:          closeTime,
		Status:             m.Status,
		Raw:                m.Raw,
	})
	if err != nil {
		return fmt.Errorf("upsert market: %w", err)
	}
	for _, o := range m.Outcomes {
		if _, err := q.UpsertOutcome(ctx, gen.UpsertOutcomeParams{
			MarketID:       marketID,
			VenueOutcomeID: o.VenueOutcomeID,
			Label:          o.Label,
		}); err != nil {
			return fmt.Errorf("upsert outcome %s: %w", o.VenueOutcomeID, err)
		}
	}
	return tx.Commit(ctx)
}

// InsertQuotes implements ingest.Sink. Duplicate (outcome, time) rows are
// silently skipped by the unique index — re-runs never duplicate.
func (s *Store) InsertQuotes(ctx context.Context, venueSlug string, quotes []ingest.Quote) (int, error) {
	inserted := 0
	for _, qt := range quotes {
		params, err := quoteParams(venueSlug, qt)
		if err != nil {
			s.log.Warn("quote rejected",
				"venue", venueSlug, "outcome", qt.VenueOutcomeID, "err", err)
			continue
		}
		n, err := s.q.InsertQuote(ctx, params)
		if err != nil {
			return inserted, fmt.Errorf("insert quote: %w", err)
		}
		inserted += int(n)

		if s.TopBook != nil {
			// Best-effort mirror; TopBook logs its own failures and never
			// returns an error into the ingest path.
			s.TopBook.Set(ctx, venueSlug, qt.VenueMarketID, qt.VenueOutcomeID, cache.Entry{
				Bid: qt.Bid, Ask: qt.Ask, Last: qt.Last, Time: qt.Time,
			})
		}
	}
	return inserted, nil
}

func quoteParams(venueSlug string, qt ingest.Quote) (gen.InsertQuoteParams, error) {
	var p gen.InsertQuoteParams
	var err error
	if p.Bid, err = NumericFromString(qt.Bid); err != nil {
		return p, fmt.Errorf("bid: %w", err)
	}
	if p.Ask, err = NumericFromString(qt.Ask); err != nil {
		return p, fmt.Errorf("ask: %w", err)
	}
	if p.Last, err = NumericFromString(qt.Last); err != nil {
		return p, fmt.Errorf("last: %w", err)
	}
	if p.Volume24h, err = NumericFromString(qt.Volume24h); err != nil {
		return p, fmt.Errorf("volume_24h: %w", err)
	}
	if p.Liquidity, err = NumericFromString(qt.Liquidity); err != nil {
		return p, fmt.Errorf("liquidity: %w", err)
	}
	p.Time = pgtype.Timestamptz{Time: qt.Time.UTC(), Valid: true}
	p.Raw = qt.Raw
	p.VenueSlug = venueSlug
	p.VenueMarketID = qt.VenueMarketID
	p.VenueOutcomeID = qt.VenueOutcomeID
	return p, nil
}

// ActiveMarketIDs implements ingest.Sink.
func (s *Store) ActiveMarketIDs(ctx context.Context, venueSlug string) ([]string, error) {
	return s.q.ActiveMarketIDs(ctx, venueSlug)
}

// VenueLag reports the newest quote per venue for /healthz.
type VenueLag struct {
	Slug        string
	LastQuoteAt *time.Time
}

func (s *Store) VenueLags(ctx context.Context) ([]VenueLag, error) {
	rows, err := s.q.VenueLag(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]VenueLag, 0, len(rows))
	for _, r := range rows {
		vl := VenueLag{Slug: r.Slug}
		if r.LastQuoteAt.Valid {
			t := r.LastQuoteAt.Time
			vl.LastQuoteAt = &t
		}
		out = append(out, vl)
	}
	return out, nil
}
