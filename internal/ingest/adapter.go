// Package ingest contains the venue-agnostic ingestion core: the Adapter
// interface every venue implements, the Supervisor that keeps one polling
// goroutine per venue alive, and the shared hardened HTTP client.
package ingest

import (
	"context"
	"encoding/json"
	"time"
)

// Market is a normalized market plus its raw venue payload. Markets are
// metadata only — prices never appear here.
type Market struct {
	VenueMarketID      string
	Title              string
	ResolutionCriteria string
	CloseTime          *time.Time // nil if the venue did not provide one
	Status             string     // normalized: "active" | "closed"
	Raw                json.RawMessage
	Outcomes           []Outcome
}

// Outcome is one tradable side of a market (e.g. Yes/No, or a candidate).
type Outcome struct {
	VenueOutcomeID string
	Label          string
}

// Quote is one top-of-book snapshot for a single outcome.
//
// WHY prices are strings here: float64 silently mangles decimals
// (0.57 becomes 0.56999...). Venue payloads arrive as JSON strings or
// json.Number; we carry the exact decimal text end-to-end and convert to
// Postgres NUMERIC via pgtype only at the store boundary. Empty string
// means NULL (venue had no value), never zero.
type Quote struct {
	VenueMarketID  string
	VenueOutcomeID string
	Time           time.Time
	Bid            string
	Ask            string
	Last           string
	Volume24h      string
	Liquidity      string
	Raw            json.RawMessage
}

// Adapter is the one-per-venue contract. Implementations must be safe for
// concurrent use: the supervisor calls FetchMarkets and FetchQuotes from
// the same venue goroutine, but Health may be called from the API.
type Adapter interface {
	// FetchMarkets returns the venue's currently-listed markets worth
	// ingesting (active, not yet closed), with raw payloads attached.
	FetchMarkets(ctx context.Context) ([]Market, error)

	// FetchQuotes returns top-of-book snapshots for the given
	// venue_market_ids (as returned by FetchMarkets).
	FetchQuotes(ctx context.Context, venueMarketIDs []string) ([]Quote, error)

	// Health performs a cheap liveness probe against the venue API.
	Health(ctx context.Context) error

	// Slug is the venue's stable identifier, matching venues.slug.
	Slug() string
}
