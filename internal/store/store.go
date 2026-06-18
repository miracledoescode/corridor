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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
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

	// priceMu guards lastPx, the write-time dedup cache: for each outcome,
	// the price of the most recent quote we stored. The two venue loops call
	// InsertQuotes concurrently, so this must be locked.
	priceMu sync.Mutex
	lastPx  map[string]pricePoint
}

// pricePoint is an outcome's last-stored price, as scale-normalized keys so
// equal values compare equal regardless of source formatting.
type pricePoint struct{ bid, ask, last string }

// pxKey identifies an outcome the same way the InsertQuote query resolves it
// (venue slug + venue's market id + venue's outcome id), so the dedup cache
// and the DB write agree on identity without needing surrogate ids in memory.
func pxKey(venueSlug, venueMarketID, venueOutcomeID string) string {
	return venueSlug + "\x1f" + venueMarketID + "\x1f" + venueOutcomeID
}

// maxPoolConns caps concurrent connections corridord opens.
//
// WHY a cap, and WHY the pooler: prod is Supabase. Connect via the
// transaction POOLER (host ...pooler.supabase.com, port 6543), never the
// direct port-5432 endpoint. The supervisor opens a connection per venue
// plus migration/health traffic, and Supabase's direct endpoint allows
// only a few dozen total server connections — a handful of restarting
// services would exhaust them and stall ingestion (a prime-directive
// violation). The pooler multiplexes our pool over a small set of real
// backends, so 15 app-side conns is comfortable and predictable. We honor
// a lower pool_max_conns from the URL but never exceed this ceiling.
const maxPoolConns = 15

func New(ctx context.Context, dbURL string, log *slog.Logger) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	if cfg.MaxConns <= 0 || cfg.MaxConns > maxPoolConns {
		cfg.MaxConns = maxPoolConns
	}
	// WHY simple protocol: prod runs behind Supabase's TRANSACTION-mode
	// pooler (port 6543), which does not guarantee that successive round
	// trips on one logical connection land on the same backend. pgx's
	// default mode (and the describe/exec modes) lean on server-side
	// prepared statements — a statement is prepared on backend A, then the
	// pooler routes the execute to backend B where it doesn't exist:
	// "unnamed prepared statement does not exist" (SQLSTATE 26000), which
	// is exactly what crashed the Kalshi sweep on cutover. Simple protocol
	// uses NO prepared statements at all — one Query message with the args
	// formatted (safely, pgx-escaped) inline — so there is nothing for the
	// pooler to lose between trips. The cost is no binary format / no
	// server-side statement reuse; fine for a write-heavy ingest path.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	// WHY this hook: simple protocol can't ask the server for a parameter's
	// type, so pgx infers it from the Go value — and a Go []byte defaults to
	// bytea, which corrupts our jsonb `raw` columns (markets.raw, quotes.raw
	// — the only []byte arguments we send). Registering []byte as jsonb makes
	// the encoding correct. Safe because we have zero bytea columns. Runs on
	// every pooled connection at creation; connection-init only.
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterDefaultPgType([]byte(nil), "jsonb")
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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
		lastPx:   make(map[string]pricePoint),
	}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate runs the embedded goose migrations. Goose tracks applied
// versions, so calling this on every boot is idempotent.
func Migrate(dbURL string) error {
	// Same pooler-safety as the runtime pool: goose runs DDL through
	// database/sql, which also leans on prepared statements by default and
	// would hit the same 26000 behind the Supabase transaction pooler. Open
	// database/sql via a pgx conn config in simple-protocol mode. No jsonb
	// params here (migrations are DDL + integer/timestamp version rows), so
	// the []byte->jsonb hook the runtime pool needs is unnecessary.
	connCfg, err := pgx.ParseConfig(dbURL)
	if err != nil {
		return err
	}
	connCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db := sql.OpenDB(stdlib.GetConnector(*connCfg))
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

// UpsertMarkets implements ingest.Sink. All markets and their outcomes are
// written in two pipelined batches (markets, then outcomes) inside ONE
// transaction, so the whole metadata sweep is ~2 network round trips instead
// of ~4 per market.
//
// WHY this shape: corridord runs against a REMOTE Supabase. The old
// per-market transaction (Begin → upsert market → upsert each outcome →
// Commit) was ~4 round trips × hundreds of markets ≈ tens of seconds of pure
// network latency every metadata cycle — and because that ran in the venue's
// goroutine it FROZE quote ingestion for that whole stretch (polymarket lost
// ~40-50s of price history per minute). Batching collapses it to ~2 round
// trips. The transaction keeps it atomic (no market visible without its
// outcomes), and the outcome batch resolves market_id inline so it needs no
// id handed back from the market batch. Pooler-safe: the transaction pins one
// backend, and the pool's simple-protocol mode sends each batch as one query.
//
// Trade-off vs the old loop: a DB error fails the whole cycle instead of
// skipping one market. Acceptable — adapters pre-filter malformed markets
// before they reach here, and a failed metadata cycle just retries (quotes,
// in their own loop, are unaffected).
func (s *Store) UpsertMarkets(ctx context.Context, venueSlug string, markets []ingest.Market) (int, error) {
	if len(markets) == 0 {
		return 0, nil
	}
	venueID, err := s.venueID(ctx, venueSlug)
	if err != nil {
		return 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	marketParams := make([]gen.UpsertMarketBatchParams, len(markets))
	var outcomeParams []gen.UpsertOutcomeBatchParams
	for i, m := range markets {
		closeTime := pgtype.Timestamptz{}
		if m.CloseTime != nil {
			closeTime = pgtype.Timestamptz{Time: *m.CloseTime, Valid: true}
		}
		marketParams[i] = gen.UpsertMarketBatchParams{
			VenueID:            venueID,
			VenueMarketID:      m.VenueMarketID,
			Title:              m.Title,
			ResolutionCriteria: pgtype.Text{String: m.ResolutionCriteria, Valid: m.ResolutionCriteria != ""},
			CloseTime:          closeTime,
			Status:             m.Status,
			Raw:                m.Raw,
		}
		for _, o := range m.Outcomes {
			outcomeParams = append(outcomeParams, gen.UpsertOutcomeBatchParams{
				VenueID:        venueID,
				VenueMarketID:  m.VenueMarketID,
				VenueOutcomeID: o.VenueOutcomeID,
				Label:          o.Label,
			})
		}
	}

	if err := execBatch(qtx.UpsertMarketBatch(ctx, marketParams)); err != nil {
		return 0, fmt.Errorf("upsert markets batch: %w", err)
	}
	if len(outcomeParams) > 0 {
		if err := execBatch(qtx.UpsertOutcomeBatch(ctx, outcomeParams)); err != nil {
			return 0, fmt.Errorf("upsert outcomes batch: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit markets: %w", err)
	}
	return len(markets), nil
}

// batchExecResult is the common shape sqlc generates for a :batchexec result
// (UpsertMarketBatch / UpsertOutcomeBatch). execBatch drains one, returning
// the first per-statement error (or a Close error) so the caller can fail the
// transaction.
type batchExecResult interface {
	Exec(func(int, error))
	Close() error
}

func execBatch(r batchExecResult) error {
	var firstErr error
	r.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if closeErr := r.Close(); closeErr != nil && firstErr == nil {
		firstErr = closeErr
	}
	return firstErr
}

// InsertQuotes implements ingest.Sink. It persists a quote row only when the
// price CHANGED since the outcome's last stored quote; an unchanged price is
// skipped. Returns the number of rows actually written.
//
// WHY write-time dedup (Phase 1.5): we poll top-of-book every ~10s, but
// prediction-market prices sit still for long stretches. Writing a row every
// cycle regardless grew quotes by ~1.8M rows/day and would cross the Supabase
// free-tier cap in days → the project flips read-only → ingestion writes fail
// (the prime-directive nightmare). Storing only changes cuts volume 5-20×.
//
// SEMANTICS: quotes now means "price CHANGES", not "samples at fixed
// intervals". The price at any time T is the most recent quote row at or
// before T (carry the last value forward) — NOT a row stamped exactly at T.
// Everything downstream (matcher, spread engine, charts) must reconstruct a
// point-in-time price as "latest quote <= T". Liveness ("is ingestion
// alive?") moved to the venues.last_polled_at heartbeat, because MAX(q.time)
// no longer advances when prices are stable.
//
// WHY only bid/ask/last gate the write (not volume_24h / liquidity): those
// drift almost every cycle and would defeat dedup entirely. We do not write a
// row just because volume moved; the latest volume/liquidity rides along
// opportunistically on whatever row a price change produces.
func (s *Store) InsertQuotes(ctx context.Context, venueSlug string, quotes []ingest.Quote) (int, error) {
	inserted := 0
	for _, qt := range quotes {
		params, err := quoteParams(venueSlug, qt)
		if err != nil {
			s.log.Warn("quote rejected",
				"venue", venueSlug, "outcome", qt.VenueOutcomeID, "err", err)
			continue
		}

		// Always refresh the live top-of-book cache, even on an unchanged
		// price: Redis entries carry a TTL, so skipping the refresh during a
		// stable stretch would let the "current price" expire out of the
		// cache. Dedup applies only to the durable quotes table, never to the
		// live cache. Best-effort; TopBook logs its own failures.
		if s.TopBook != nil {
			s.TopBook.Set(ctx, venueSlug, qt.VenueMarketID, qt.VenueOutcomeID, cache.Entry{
				Bid: qt.Bid, Ask: qt.Ask, Last: qt.Last, Time: qt.Time,
			})
		}

		key := pxKey(venueSlug, qt.VenueMarketID, qt.VenueOutcomeID)
		cur := pricePoint{
			bid:  canonicalKey(params.Bid),
			ask:  canonicalKey(params.Ask),
			last: canonicalKey(params.Last),
		}
		s.priceMu.Lock()
		prev, seen := s.lastPx[key]
		s.priceMu.Unlock()
		if seen && prev == cur {
			continue // price unchanged → no new row
		}

		n, err := s.q.InsertQuote(ctx, params)
		if err != nil {
			return inserted, fmt.Errorf("insert quote: %w", err)
		}
		// Record what we just stored as this outcome's latest price. We update
		// the cache on a write attempt (not only when n==1): n==0 means the
		// (outcome, time) row already existed — the stored value still equals
		// cur, so the cache stays correct.
		s.priceMu.Lock()
		s.lastPx[key] = cur
		s.priceMu.Unlock()
		inserted += int(n)
	}
	return inserted, nil
}

// WarmDedupCache seeds the in-memory dedup cache from the newest stored quote
// per outcome. WHY: without it, a restart starts with an empty cache and the
// first poll re-writes a row for every outcome (a few thousand redundant rows
// per restart) before dedup kicks in. Best-effort by design — a failure just
// means that one-time redundant write, never a fatal boot error.
func (s *Store) WarmDedupCache(ctx context.Context) error {
	rows, err := s.q.LatestQuotePerOutcome(ctx)
	if err != nil {
		return err
	}
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	for _, r := range rows {
		// COALESCE in the query maps a NULL price to "", which
		// NumericFromString turns back into a NULL Numeric — same as the live
		// path, so the keys match. Parse errors can't happen on text we wrote.
		bid, _ := NumericFromString(r.Bid)
		ask, _ := NumericFromString(r.Ask)
		last, _ := NumericFromString(r.Last)
		s.lastPx[pxKey(r.VenueSlug, r.VenueMarketID, r.VenueOutcomeID)] = pricePoint{
			bid:  canonicalKey(bid),
			ask:  canonicalKey(ask),
			last: canonicalKey(last),
		}
	}
	s.log.Info("dedup cache warmed", "outcomes", len(rows))
	return nil
}

// MarkVenuePolled implements ingest.Sink: stamp the venue heartbeat at the end
// of every quote cycle so /healthz and `make verify` can tell a stable-price
// venue (healthy, no new quotes) from a dead one. See migration 003.
func (s *Store) MarkVenuePolled(ctx context.Context, venueSlug string) error {
	return s.q.MarkVenuePolled(ctx, venueSlug)
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
	// WHY no raw on quotes: the per-quote raw JSONB payload was ~90% of the
	// quotes table's storage (508MB/day, over the Supabase free tier) and is
	// never read — the trusted parser already extracts bid/ask/last/volume/
	// liquidity into typed columns. We leave the column in the schema but
	// write NULL. markets.raw STAYS: re-parsing market metadata has real
	// value and that table is tiny by comparison.
	p.Raw = nil
	p.VenueSlug = venueSlug
	p.VenueMarketID = qt.VenueMarketID
	p.VenueOutcomeID = qt.VenueOutcomeID
	return p, nil
}

// ActiveMarketIDs implements ingest.Sink.
func (s *Store) ActiveMarketIDs(ctx context.Context, venueSlug string) ([]string, error) {
	return s.q.ActiveMarketIDs(ctx, venueSlug)
}

// DeleteQuotesOlderThan removes quote rows older than days and returns how
// many were deleted. Used by the retention job to keep the quotes table
// within the Supabase free-tier storage cap.
//
// WHY make_interval over '($1 || ” days”)::interval: it takes the day
// count as an integer parameter directly — no string concatenation, and it
// rides the pool's simple-protocol mode cleanly (the arg is formatted
// inline by pgx, no prepared statement for the pooler to lose).
func (s *Store) DeleteQuotesOlderThan(ctx context.Context, days int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM quotes WHERE time < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// VenueLag reports a venue's liveness for /healthz. LastPolledAt is the
// heartbeat (advances every cycle = the health signal); LastQuoteAt is the
// last actual price change (may be old when prices are stable = informational).
type VenueLag struct {
	Slug         string
	LastPolledAt *time.Time
	LastQuoteAt  *time.Time
}

func (s *Store) VenueLags(ctx context.Context) ([]VenueLag, error) {
	rows, err := s.q.VenueLag(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]VenueLag, 0, len(rows))
	for _, r := range rows {
		vl := VenueLag{Slug: r.Slug}
		if r.LastPolledAt.Valid {
			t := r.LastPolledAt.Time
			vl.LastPolledAt = &t
		}
		if r.LastQuoteAt.Valid {
			t := r.LastQuoteAt.Time
			vl.LastQuoteAt = &t
		}
		out = append(out, vl)
	}
	return out, nil
}
