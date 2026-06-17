package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/miracledoescode/corridor/internal/ingest"
)

// TestIdempotentIngest proves the "re-runs never duplicate" hard rule
// against a real database. Run with:
//
//	TEST_DB_URL=postgres://corridor:corridor@localhost:5432/corridor?sslmode=disable go test ./internal/store/
//
// Skipped when TEST_DB_URL is unset so `go test ./...` stays green
// without infrastructure.
func TestIdempotentIngest(t *testing.T) {
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set")
	}

	ctx := context.Background()
	if err := Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := New(ctx, dbURL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	closeTime := time.Now().Add(24 * time.Hour).UTC()
	market := ingest.Market{
		VenueMarketID:      "itest-market-1",
		Title:              "integration test market",
		ResolutionCriteria: "resolves YES in tests",
		CloseTime:          &closeTime,
		Status:             "active",
		Raw:                json.RawMessage(`{"fixture":true}`),
		Outcomes: []ingest.Outcome{
			{VenueOutcomeID: "itest-yes", Label: "Yes"},
			{VenueOutcomeID: "itest-no", Label: "No"},
		},
	}

	// Upserting the same market twice must not duplicate.
	for i := 0; i < 2; i++ {
		if _, err := st.UpsertMarkets(ctx, "polymarket", []ingest.Market{market}); err != nil {
			t.Fatalf("upsert round %d: %v", i+1, err)
		}
	}
	var marketCount int
	err = st.pool.QueryRow(ctx,
		`SELECT count(*) FROM markets WHERE venue_market_id = 'itest-market-1'`).Scan(&marketCount)
	if err != nil {
		t.Fatal(err)
	}
	if marketCount != 1 {
		t.Fatalf("market rows = %d, want 1", marketCount)
	}

	// Inserting the same quote snapshot twice must not duplicate.
	q := ingest.Quote{
		VenueMarketID:  "itest-market-1",
		VenueOutcomeID: "itest-yes",
		Time:           time.Now().UTC().Truncate(time.Second),
		Bid:            "0.57",
		Ask:            "0.59",
		Volume24h:      "1200",
		Liquidity:      "25099.23",
		Raw:            json.RawMessage(`{"fixture":true}`),
	}
	n1, err := st.InsertQuotes(ctx, "polymarket", []ingest.Quote{q})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := st.InsertQuotes(ctx, "polymarket", []ingest.Quote{q})
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 1 || n2 != 0 {
		t.Fatalf("inserted %d then %d rows, want 1 then 0", n1, n2)
	}

	// The price must round-trip exactly — NUMERIC, not float.
	var bid string
	err = st.pool.QueryRow(ctx,
		`SELECT bid::text FROM quotes q JOIN outcomes o ON o.id = q.outcome_id
		 WHERE o.venue_outcome_id = 'itest-yes' ORDER BY q.time DESC LIMIT 1`).Scan(&bid)
	if err != nil {
		t.Fatal(err)
	}
	if bid != "0.57000" { // NUMERIC(8,5)
		t.Errorf("bid round-trip = %q, want 0.57000", bid)
	}

	// quotes.raw must be NULL even though the source Quote carried a payload:
	// we stopped storing per-quote raw to stay under the storage cap.
	var rawNull bool
	err = st.pool.QueryRow(ctx,
		`SELECT raw IS NULL FROM quotes q JOIN outcomes o ON o.id = q.outcome_id
		 WHERE o.venue_outcome_id = 'itest-yes' ORDER BY q.time DESC LIMIT 1`).Scan(&rawNull)
	if err != nil {
		t.Fatal(err)
	}
	if !rawNull {
		t.Error("quotes.raw should be NULL (raw payload no longer stored)")
	}

	// Write-time dedup: the same price at a NEW timestamp must NOT write a row;
	// a changed price must. (q above already seeded bid 0.57 in the cache.)
	same := q
	same.Time = q.Time.Add(30 * time.Second)
	if n, err := st.InsertQuotes(ctx, "polymarket", []ingest.Quote{same}); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("unchanged price wrote %d rows, want 0 (dedup)", n)
	}
	moved := q
	moved.Time = q.Time.Add(60 * time.Second)
	moved.Bid = "0.61" // price moved
	if n, err := st.InsertQuotes(ctx, "polymarket", []ingest.Quote{moved}); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Errorf("changed price wrote %d rows, want 1", n)
	}

	// Warm a FRESH store from the DB: the last stored price (0.61) must land in
	// the rebuilt cache, so re-submitting it dedups rather than writing.
	st2, err := New(ctx, dbURL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := st2.WarmDedupCache(ctx); err != nil {
		t.Fatal(err)
	}
	warm := moved
	warm.Time = moved.Time.Add(30 * time.Second)
	if n, err := st2.InsertQuotes(ctx, "polymarket", []ingest.Quote{warm}); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("warmed cache wrote %d rows, want 0 (price unchanged across restart)", n)
	}
	st2.Close()

	// Heartbeat: MarkVenuePolled stamps venues.last_polled_at fresh.
	if err := st.MarkVenuePolled(ctx, "polymarket"); err != nil {
		t.Fatal(err)
	}
	var polledLag float64
	err = st.pool.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM (now() - last_polled_at)) FROM venues WHERE slug='polymarket'`).Scan(&polledLag)
	if err != nil {
		t.Fatal(err)
	}
	if polledLag > 60 {
		t.Errorf("last_polled_at lag = %.0fs, want fresh (<60s)", polledLag)
	}

	// Retention: a quote older than the cutoff is pruned, fresh ones kept. By
	// now itest-yes has two fresh rows (0.57 @ t, 0.61 @ t+60s); the deduped
	// and warm submissions wrote nothing.
	old := ingest.Quote{
		VenueMarketID:  "itest-market-1",
		VenueOutcomeID: "itest-yes",
		Time:           time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second),
		Bid:            "0.10",
	}
	if _, err := st.InsertQuotes(ctx, "polymarket", []ingest.Quote{old}); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.DeleteQuotesOlderThan(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 1 {
		t.Errorf("retention deleted %d rows, want >=1 (the 10-day-old quote)", deleted)
	}
	var remaining int
	err = st.pool.QueryRow(ctx,
		`SELECT count(*) FROM quotes q JOIN outcomes o ON o.id = q.outcome_id
		 WHERE o.venue_outcome_id = 'itest-yes'`).Scan(&remaining)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 2 { // the two fresh price points survive
		t.Errorf("after retention %d quotes remain, want 2 (the fresh price points)", remaining)
	}

	// Cleanup so repeated test runs stay deterministic.
	_, _ = st.pool.Exec(ctx, `DELETE FROM quotes WHERE outcome_id IN
		(SELECT id FROM outcomes WHERE venue_outcome_id LIKE 'itest-%')`)
	_, _ = st.pool.Exec(ctx, `DELETE FROM outcomes WHERE venue_outcome_id LIKE 'itest-%'`)
	_, _ = st.pool.Exec(ctx, `DELETE FROM markets WHERE venue_market_id = 'itest-market-1'`)
}
