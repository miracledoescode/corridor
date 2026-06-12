// Package polymarket ingests markets from the Gamma API and top-of-book
// from the CLOB API.
package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/miracledoescode/corridor/internal/ingest"
)

const (
	slug = "polymarket"

	// Hard politeness cap from the brief: max 1 req/s across every
	// Polymarket endpoint.
	requestsPerSecond = 1.0

	pageSize = 100

	// maxMarkets bounds the metadata sweep (and with it the quote sweep)
	// so the request budget stays sane under the 1 req/s cap: 5 pages of
	// metadata, and ~2 tokens per market → ~10 batched book requests per
	// quote sweep. Gamma is asked for markets ordered by 24h volume
	// descending, so the cap keeps the most liquid markets — the ones a
	// day trader can actually arb.
	maxMarkets = 500

	// booksBatchSize is how many token_ids go into one POST /books call.
	booksBatchSize = 100
)

// Adapter implements ingest.Adapter for Polymarket.
type Adapter struct {
	gammaURL string
	clobURL  string
	client   *ingest.Client
	log      *slog.Logger

	// Metadata poll output cached for the quote poll: which CLOB tokens
	// belong to which market, and market-level volume/liquidity figures
	// (Gamma reports them per market; the book endpoint has neither).
	mu             sync.RWMutex
	tokensByMkt    map[string][]string
	mktByToken     map[string]string
	volumeByMkt    map[string]string
	liquidityByMkt map[string]string
}

func New(gammaURL, clobURL, userAgent string, log *slog.Logger) *Adapter {
	return &Adapter{
		gammaURL:       strings.TrimRight(gammaURL, "/"),
		clobURL:        strings.TrimRight(clobURL, "/"),
		client:         ingest.NewClient(userAgent, requestsPerSecond),
		log:            log.With("venue", slug),
		tokensByMkt:    make(map[string][]string),
		mktByToken:     make(map[string]string),
		volumeByMkt:    make(map[string]string),
		liquidityByMkt: make(map[string]string),
	}
}

func (a *Adapter) Slug() string { return slug }

// flexNum tolerates Gamma's habit of sending the same field as a JSON
// number, a quoted string, or null, and preserves the exact decimal text
// either way (never parsed as float). Garbage is rejected later by the
// store's NumericFromString.
type flexNum string

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexNum(s)
	return nil
}

type gammaMarket struct {
	ID           string  `json:"id"`
	Question     string  `json:"question"`
	Description  string  `json:"description"`
	EndDate      string  `json:"endDate"`
	Active       bool    `json:"active"`
	Closed       bool    `json:"closed"`
	Outcomes     string  `json:"outcomes"`     // JSON-encoded array, e.g. "[\"Yes\",\"No\"]"
	ClobTokenIDs string  `json:"clobTokenIds"` // JSON-encoded array of token ids
	Volume24hr   flexNum `json:"volume24hr"`
	Liquidity    flexNum `json:"liquidityNum"`
}

// FetchMarkets pages Gamma /markets, keeping only markets that are
// active, not closed, and with a parseable close time in the future
// (the brief's ingest filter).
func (a *Adapter) FetchMarkets(ctx context.Context) ([]ingest.Market, error) {
	var out []ingest.Market

	tokensByMkt := make(map[string][]string)
	mktByToken := make(map[string]string)
	volumeByMkt := make(map[string]string)
	liquidityByMkt := make(map[string]string)

	for offset := 0; offset < maxMarkets; offset += pageSize {
		url := fmt.Sprintf(
			"%s/markets?limit=%d&offset=%d&active=true&closed=false&order=volume24hr&ascending=false",
			a.gammaURL, pageSize, offset)

		// Raw page first: each element is stored verbatim in markets.raw.
		var page []json.RawMessage
		if err := a.client.GetJSON(ctx, url, &page); err != nil {
			return nil, fmt.Errorf("gamma markets page offset=%d: %w", offset, err)
		}
		if len(page) == 0 {
			break
		}

		for _, raw := range page {
			var gm gammaMarket
			if err := json.Unmarshal(raw, &gm); err != nil {
				a.log.Warn("unparseable gamma market skipped", "err", err)
				continue
			}
			m, ok := a.normalize(gm, raw)
			if !ok {
				continue
			}
			out = append(out, m)

			tokens := make([]string, 0, len(m.Outcomes))
			for _, o := range m.Outcomes {
				tokens = append(tokens, o.VenueOutcomeID)
				mktByToken[o.VenueOutcomeID] = m.VenueMarketID
			}
			tokensByMkt[m.VenueMarketID] = tokens
			volumeByMkt[m.VenueMarketID] = string(gm.Volume24hr)
			liquidityByMkt[m.VenueMarketID] = string(gm.Liquidity)
		}

		if len(page) < pageSize {
			break
		}
	}

	a.mu.Lock()
	a.tokensByMkt = tokensByMkt
	a.mktByToken = mktByToken
	a.volumeByMkt = volumeByMkt
	a.liquidityByMkt = liquidityByMkt
	a.mu.Unlock()

	return out, nil
}

func (a *Adapter) normalize(gm gammaMarket, raw json.RawMessage) (ingest.Market, bool) {
	if !gm.Active || gm.Closed || gm.ID == "" {
		return ingest.Market{}, false
	}
	closeTime, err := time.Parse(time.RFC3339, gm.EndDate)
	if err != nil || !closeTime.After(time.Now()) {
		return ingest.Market{}, false
	}

	// Outcome labels and CLOB token ids arrive as two parallel
	// JSON-encoded arrays; index i of one corresponds to index i of the
	// other. A length mismatch means we cannot attribute quotes to the
	// right outcome — skip the market rather than guess.
	var labels, tokens []string
	if err := json.Unmarshal([]byte(gm.Outcomes), &labels); err != nil {
		return ingest.Market{}, false
	}
	if err := json.Unmarshal([]byte(gm.ClobTokenIDs), &tokens); err != nil {
		return ingest.Market{}, false
	}
	if len(labels) != len(tokens) || len(tokens) == 0 {
		a.log.Warn("outcome/token mismatch skipped",
			"venue_market_id", gm.ID, "labels", len(labels), "tokens", len(tokens))
		return ingest.Market{}, false
	}

	outcomes := make([]ingest.Outcome, len(tokens))
	for i := range tokens {
		outcomes[i] = ingest.Outcome{VenueOutcomeID: tokens[i], Label: labels[i]}
	}
	return ingest.Market{
		VenueMarketID:      gm.ID,
		Title:              gm.Question,
		ResolutionCriteria: gm.Description,
		CloseTime:          &closeTime,
		Status:             "active",
		Raw:                raw,
		Outcomes:           outcomes,
	}, true
}

type bookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type clobBook struct {
	AssetID string      `json:"asset_id"`
	Bids    []bookLevel `json:"bids"`
	Asks    []bookLevel `json:"asks"`
}

// FetchQuotes pulls order books for every token of the requested markets.
//
// WHY POST /books (batch) instead of the brief's GET /book?token_id=:
// same data, but one request covers 100 tokens. At the hard 1 req/s cap,
// per-token GETs would take ~17 minutes per sweep of 500 two-outcome
// markets; batching does it in ~10 seconds. Politer to the venue AND
// faster for us.
func (a *Adapter) FetchQuotes(ctx context.Context, venueMarketIDs []string) ([]ingest.Quote, error) {
	a.mu.RLock()
	var tokens []string
	unknown := 0
	for _, id := range venueMarketIDs {
		ts, ok := a.tokensByMkt[id]
		if !ok {
			unknown++ // e.g. a DB-active market that fell out of the top-N sweep
			continue
		}
		tokens = append(tokens, ts...)
	}
	a.mu.RUnlock()
	if unknown > 0 {
		a.log.Debug("markets without cached tokens skipped", "count", unknown)
	}
	if len(tokens) == 0 {
		return nil, nil
	}

	now := time.Now().UTC().Truncate(time.Second)
	var quotes []ingest.Quote

	for start := 0; start < len(tokens); start += booksBatchSize {
		end := min(start+booksBatchSize, len(tokens))
		body := make([]map[string]string, 0, end-start)
		for _, tok := range tokens[start:end] {
			body = append(body, map[string]string{"token_id": tok})
		}

		var books []json.RawMessage
		if err := a.client.PostJSON(ctx, a.clobURL+"/books", body, &books); err != nil {
			return nil, fmt.Errorf("clob books batch: %w", err)
		}

		for _, raw := range books {
			var b clobBook
			if err := json.Unmarshal(raw, &b); err != nil {
				a.log.Warn("unparseable book skipped", "err", err)
				continue
			}
			q, ok := a.bookToQuote(b, raw, now)
			if !ok {
				continue
			}
			quotes = append(quotes, q)
		}
	}
	return quotes, nil
}

func (a *Adapter) bookToQuote(b clobBook, raw json.RawMessage, now time.Time) (ingest.Quote, bool) {
	a.mu.RLock()
	mktID, ok := a.mktByToken[b.AssetID]
	vol := a.volumeByMkt[mktID]
	liq := a.liquidityByMkt[mktID]
	a.mu.RUnlock()
	if !ok {
		return ingest.Quote{}, false
	}
	return ingest.Quote{
		VenueMarketID:  mktID,
		VenueOutcomeID: b.AssetID,
		Time:           now,
		Bid:            bestPrice(b.Bids, true),
		Ask:            bestPrice(b.Asks, false),
		// last: would cost one /prices-history request per token — that
		// blows the 1 req/s budget for marginal value (bid/ask is what
		// the spread engine needs). NULL for now.
		Volume24h: vol,
		Liquidity: liq,
		Raw:       raw,
	}, true
}

// bestPrice picks the top of one book side: highest bid or lowest ask.
// WHY not trust ordering: the CLOB docs don't promise sort order, so we
// compare every level — exactly, via big.Rat, never float.
func bestPrice(levels []bookLevel, wantMax bool) string {
	best := ""
	var bestVal *big.Rat
	for _, l := range levels {
		v, ok := new(big.Rat).SetString(l.Price)
		if !ok {
			continue
		}
		if bestVal == nil || (wantMax && v.Cmp(bestVal) > 0) || (!wantMax && v.Cmp(bestVal) < 0) {
			bestVal = v
			best = l.Price
		}
	}
	return best
}

// Health probes Gamma with the cheapest possible call.
func (a *Adapter) Health(ctx context.Context) error {
	var page []json.RawMessage
	return a.client.GetJSON(ctx, a.gammaURL+"/markets?limit=1", &page)
}
