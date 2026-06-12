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

	// Cleanup so repeated test runs stay deterministic.
	_, _ = st.pool.Exec(ctx, `DELETE FROM quotes WHERE outcome_id IN
		(SELECT id FROM outcomes WHERE venue_outcome_id LIKE 'itest-%')`)
	_, _ = st.pool.Exec(ctx, `DELETE FROM outcomes WHERE venue_outcome_id LIKE 'itest-%'`)
	_, _ = st.pool.Exec(ctx, `DELETE FROM markets WHERE venue_market_id = 'itest-market-1'`)
}
