// Package kalshi ingests markets and top-of-book from the Kalshi v2
// trade API.
package kalshi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/miracledoescode/corridor/internal/ingest"
)

const (
	slug = "kalshi"

	// Same hard politeness cap as every venue: 1 req/s.
	requestsPerSecond = 1.0

	// Kalshi allows up to 1000 markets per page.
	pageSize = 1000

	// maxPages bounds a sweep; 5 pages covers every open Kalshi market
	// with room to spare and keeps a runaway cursor loop impossible.
	maxPages = 5
)

// Adapter implements ingest.Adapter for Kalshi.
type Adapter struct {
	baseURL string
	client  *ingest.Client
	log     *slog.Logger
	now     func() time.Time // injectable so fixture tests don't rot as dates pass
}

func New(baseURL, userAgent string, log *slog.Logger) *Adapter {
	return &Adapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  ingest.NewClient(userAgent, requestsPerSecond),
		log:     log.With("venue", slug),
		now:     time.Now,
	}
}

func (a *Adapter) Slug() string { return slug }

// kalshiMarket is the subset of GET /markets we normalize. Kalshi has
// shipped two price encodings over time and we must parse BOTH:
//   - legacy: integer cents (yes_bid: 57)
//   - current: dollar strings with deci-cent precision (yes_bid_dollars:
//     "0.5700") — this is what external-api returns as of 2026-06.
//
// The *_dollars strings win when present; cents are the fallback. All
// values stay decimal text end-to-end (never float64; numbers decode as
// json.Number).
type kalshiMarket struct {
	Ticker       string      `json:"ticker"`
	Title        string      `json:"title"`
	Subtitle     string      `json:"subtitle"`
	RulesPrimary string      `json:"rules_primary"`
	CloseTime    string      `json:"close_time"`
	Status       string      `json:"status"`
	YesBid       json.Number `json:"yes_bid"`
	YesAsk       json.Number `json:"yes_ask"`
	NoBid        json.Number `json:"no_bid"`
	NoAsk        json.Number `json:"no_ask"`
	LastPrice    json.Number `json:"last_price"`
	Volume24h    json.Number `json:"volume_24h"`
	Liquidity    json.Number `json:"liquidity"`

	YesBidDollars    string `json:"yes_bid_dollars"`
	YesAskDollars    string `json:"yes_ask_dollars"`
	NoBidDollars     string `json:"no_bid_dollars"`
	NoAskDollars     string `json:"no_ask_dollars"`
	LastPriceDollars string `json:"last_price_dollars"`
	Volume24hFP      string `json:"volume_24h_fp"`
	LiquidityDollars string `json:"liquidity_dollars"`
}

// isOpenStatus tolerates Kalshi's status-vocabulary drift: the GetMarkets
// QUERY enum is still "open", but the RESPONSE now reports "active"
// (older deployments said "open" in both places). Accepting only "open"
// here silently dropped every market — the 2026-06-12 zero-ingest
// incident.
func isOpenStatus(s string) bool { return s == "active" || s == "open" }

type marketsPage struct {
	Cursor  string            `json:"cursor"`
	Markets []json.RawMessage `json:"markets"`
}

// fetchPages walks GET /markets?status=open with cursor pagination and
// hands every raw market element to fn.
func (a *Adapter) fetchPages(ctx context.Context, fn func(raw json.RawMessage, m kalshiMarket)) error {
	cursor := ""
	for page := 0; page < maxPages; page++ {
		u := fmt.Sprintf("%s/markets?limit=%d&status=open", a.baseURL, pageSize)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		var p marketsPage
		if err := a.client.GetJSON(ctx, u, &p); err != nil {
			return fmt.Errorf("kalshi markets page %d: %w", page, err)
		}
		for _, raw := range p.Markets {
			var m kalshiMarket
			if err := json.Unmarshal(raw, &m); err != nil {
				a.log.Warn("unparseable kalshi market skipped", "err", err)
				continue
			}
			fn(raw, m)
		}
		if p.Cursor == "" || len(p.Markets) == 0 {
			return nil
		}
		cursor = p.Cursor
	}
	return nil
}

// FetchMarkets returns open Kalshi markets with a close time in the
// future. Every Kalshi market is binary, so each gets a yes and a no
// outcome ("yes"/"no" are stable per-market outcome ids).
func (a *Adapter) FetchMarkets(ctx context.Context) ([]ingest.Market, error) {
	var out []ingest.Market
	err := a.fetchPages(ctx, func(raw json.RawMessage, m kalshiMarket) {
		if m.Ticker == "" || !isOpenStatus(m.Status) {
			return
		}
		closeTime, err := time.Parse(time.RFC3339, m.CloseTime)
		if err != nil || !closeTime.After(a.now()) {
			return
		}
		title := m.Title
		if m.Subtitle != "" {
			title = m.Title + " — " + m.Subtitle
		}
		out = append(out, ingest.Market{
			VenueMarketID:      m.Ticker,
			Title:              title,
			ResolutionCriteria: m.RulesPrimary,
			CloseTime:          &closeTime,
			Status:             "active", // normalized; kalshi says "active" (was "open")
			Raw:                raw,
			Outcomes: []ingest.Outcome{
				{VenueOutcomeID: "yes", Label: "Yes"},
				{VenueOutcomeID: "no", Label: "No"},
			},
		})
	})
	return out, err
}

// FetchQuotes re-walks GET /markets and emits quotes for the requested
// tickers.
//
// WHY not GET /markets/{ticker}/orderbook: the market list already
// carries top-of-book (yes_bid/yes_ask/no_bid/no_ask). A few paginated
// calls per sweep replace hundreds of per-ticker orderbook calls —
// politer to the venue, and the only way a 10s quote cadence fits under
// the 1 req/s cap.
func (a *Adapter) FetchQuotes(ctx context.Context, venueMarketIDs []string) ([]ingest.Quote, error) {
	want := make(map[string]bool, len(venueMarketIDs))
	for _, id := range venueMarketIDs {
		want[id] = true
	}

	now := a.now().UTC().Truncate(time.Second)
	var quotes []ingest.Quote
	err := a.fetchPages(ctx, func(raw json.RawMessage, m kalshiMarket) {
		if !want[m.Ticker] {
			return
		}
		// Contracts traded; the _fp (fixed-point decimal string) form
		// replaced the integer field in the current payload.
		volume := m.Volume24hFP
		if volume == "" {
			volume = string(m.Volume24h)
		}
		// Liquidity: dollars string when present, else legacy integer
		// cents converted to dollars — cross-venue comparable either way.
		liquidity := m.LiquidityDollars
		if liquidity == "" {
			var err error
			liquidity, err = centsToDollars(m.Liquidity, false)
			if err != nil {
				a.log.Warn("bad liquidity skipped", "ticker", m.Ticker, "err", err)
				liquidity = ""
			}
		}
		yes, no := quoteSides(m)
		quotes = append(quotes,
			ingest.Quote{
				VenueMarketID: m.Ticker, VenueOutcomeID: "yes", Time: now,
				Bid: yes.bid, Ask: yes.ask, Last: yes.last,
				Volume24h: volume, Liquidity: liquidity, Raw: raw,
			},
			ingest.Quote{
				VenueMarketID: m.Ticker, VenueOutcomeID: "no", Time: now,
				Bid: no.bid, Ask: no.ask, Last: no.last,
				Volume24h: volume, Liquidity: liquidity, Raw: raw,
			},
		)
	})
	return quotes, err
}

type side struct{ bid, ask, last string }

// quoteSides extracts top-of-book prices, preferring the current
// *_dollars string fields and falling back to legacy integer cents.
// A price of 0 (in either form) means "no order on that side of the
// book", so it maps to NULL rather than a fake 0.00 quote; the raw
// payload keeps the original zeros. last_price belongs to the yes side
// only.
func quoteSides(m kalshiMarket) (yes, no side) {
	conv := func(dollars string, cents json.Number) string {
		if dollars != "" {
			if isZeroDecimal(dollars) {
				return ""
			}
			return dollars
		}
		s, err := centsToDollars(cents, true)
		if err != nil {
			return ""
		}
		return s
	}
	yes = side{
		bid:  conv(m.YesBidDollars, m.YesBid),
		ask:  conv(m.YesAskDollars, m.YesAsk),
		last: conv(m.LastPriceDollars, m.LastPrice),
	}
	no = side{
		bid: conv(m.NoBidDollars, m.NoBid),
		ask: conv(m.NoAskDollars, m.NoAsk),
	}
	return yes, no
}

// isZeroDecimal reports whether decimal text like "0", "0.00", "0.0000"
// is numerically zero — by character inspection, never via float. Any
// digit other than 0 means non-zero ("0.0020" → false).
func isZeroDecimal(s string) bool {
	return s != "" && strings.Trim(s, "0.") == ""
}

// centsToDollars turns an integer cent count into an exact decimal
// string: 57 → "0.57". Pure integer math — the never-float rule.
// With nullZero, 0 returns "" (SQL NULL).
func centsToDollars(n json.Number, nullZero bool) (string, error) {
	s := n.String()
	if s == "" {
		return "", nil
	}
	c, err := n.Int64()
	if err != nil {
		return "", fmt.Errorf("non-integer cents %q: %w", s, err)
	}
	if c < 0 {
		return "", fmt.Errorf("negative cents %d", c)
	}
	if c == 0 {
		if nullZero {
			return "", nil
		}
		return "0.00", nil
	}
	return fmt.Sprintf("%d.%02d", c/100, c%100), nil
}

// Health probes the exchange status endpoint.
func (a *Adapter) Health(ctx context.Context) error {
	var out map[string]any
	return a.client.GetJSON(ctx, a.baseURL+"/exchange/status", &out)
}
