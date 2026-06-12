package kalshi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/miracledoescode/corridor/internal/ingest"
)

func TestCentsToDollars(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		nullZero bool
		want     string
		wantErr  bool
	}{
		{"typical price", "57", true, "0.57", false},
		{"single digit cents", "7", true, "0.07", false},
		{"a dollar", "100", true, "1.00", false},
		{"large liquidity", "2509923", false, "25099.23", false},
		{"zero with nullZero is NULL", "0", true, "", false},
		{"zero without nullZero is 0.00", "0", false, "0.00", false},
		{"empty is NULL", "", true, "", false},
		{"fractional cents rejected", "56.5", true, "", true},
		{"negative rejected", "-3", true, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := centsToDollars(json.Number(tt.in), tt.nullZero)
			if (err != nil) != tt.wantErr {
				t.Fatalf("centsToDollars(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("centsToDollars(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseLiveFixture is the regression test for the 2026-06-12
// zero-ingest incident: the verbatim production payload (saved from a
// live curl of external-api.kalshi.com) must parse into markets and
// quotes. It locks in both breaking changes at once: response status
// "active" (we only accepted "open") and *_dollars string prices (the
// integer-cent fields we parsed are gone).
func TestParseLiveFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/markets_live_2026-06-12.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture only carries a cursor for the next page; serve it once,
	// then an empty page to terminate pagination.
	served := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if served {
			fmt.Fprint(w, `{"cursor":"","markets":[]}`)
			return
		}
		served = true
		w.Write(body)
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv.URL)
	// Freeze the clock at capture time so the fixture's close_times stay
	// "in the future" forever.
	a.now = func() time.Time { return time.Date(2026, 6, 12, 20, 5, 0, 0, time.UTC) }

	markets, err := a.FetchMarkets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(markets) != 2 {
		t.Fatalf("got %d markets from live payload, want 2", len(markets))
	}
	for _, m := range markets {
		if m.Status != "active" || len(m.Outcomes) != 2 || m.Raw == nil {
			t.Errorf("market %s not normalized: status=%q outcomes=%d raw=%v",
				m.VenueMarketID, m.Status, len(m.Outcomes), m.Raw != nil)
		}
	}

	served = false
	mlb := "KXMVESPORTSMULTIGAMEEXTENDED-S2026DF7EF4475C3-20067410492"
	quotes, err := a.FetchQuotes(context.Background(), []string{mlb})
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 {
		t.Fatalf("got %d quotes, want 2 (yes+no)", len(quotes))
	}
	byOutcome := map[string]ingest.Quote{}
	for _, q := range quotes {
		byOutcome[q.VenueOutcomeID] = q
	}
	yes := byOutcome["yes"]
	// yes_bid_dollars "0.0000" = empty book side → NULL; ask is a real
	// deci-cent price and must survive verbatim.
	if yes.Bid != "" || yes.Ask != "0.0020" || yes.Last != "" {
		t.Errorf("yes side = %q/%q/%q, want NULL/0.0020/NULL", yes.Bid, yes.Ask, yes.Last)
	}
	no := byOutcome["no"]
	if no.Bid != "0.9980" || no.Ask != "1.0000" {
		t.Errorf("no side = %q/%q, want 0.9980/1.0000", no.Bid, no.Ask)
	}
	if yes.Volume24h != "0.00" || yes.Liquidity != "0.0000" {
		t.Errorf("stats = vol %q liq %q, want 0.00 / 0.0000 (fp/dollars fields)", yes.Volume24h, yes.Liquidity)
	}
}

func TestIsZeroDecimal(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"0", true}, {"0.00", true}, {"0.0000", true}, {"000.0", true},
		{"0.0020", false}, {"1.0000", false}, {"0.5700", false}, {"", false},
	}
	for _, tt := range tests {
		if got := isZeroDecimal(tt.in); got != tt.want {
			t.Errorf("isZeroDecimal(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func marketsFixture(future, past string) string {
	return fmt.Sprintf(`{"cursor":"","markets":[
	  {"ticker":"INX-26DEC31","title":"S&P closes above 6000","subtitle":"on Dec 31",
	   "rules_primary":"Resolves YES if ...","close_time":"%s","status":"open",
	   "yes_bid":57,"yes_ask":59,"no_bid":41,"no_ask":43,"last_price":58,
	   "volume_24h":1200,"liquidity":2509923},
	  {"ticker":"EMPTYBOOK","title":"No orders yet","subtitle":"",
	   "rules_primary":"","close_time":"%s","status":"open",
	   "yes_bid":0,"yes_ask":0,"no_bid":0,"no_ask":0,"last_price":0,
	   "volume_24h":0,"liquidity":0},
	  {"ticker":"SETTLED","title":"Old market","subtitle":"",
	   "rules_primary":"","close_time":"%s","status":"settled",
	   "yes_bid":0,"yes_ask":0,"no_bid":0,"no_ask":0,"last_price":0,
	   "volume_24h":0,"liquidity":0},
	  {"ticker":"EXPIRED","title":"Past close","subtitle":"",
	   "rules_primary":"","close_time":"%s","status":"open",
	   "yes_bid":10,"yes_ask":12,"no_bid":88,"no_ask":90,"last_price":11,
	   "volume_24h":5,"liquidity":100}
	]}`, future, future, past, past)
}

func newTestAdapter(t *testing.T, base string) *Adapter {
	t.Helper()
	a := New(base, "test-agent", slog.Default())
	a.client = ingest.NewClient("test-agent", 10000) // production 1 req/s cap is too slow for tests
	return a
}

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/markets" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, marketsFixture(future, past))
	}))
}

func TestFetchMarkets(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	a := newTestAdapter(t, srv.URL)
	markets, err := a.FetchMarkets(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// INX survives; EMPTYBOOK survives (open, future close);
	// SETTLED filtered on status; EXPIRED filtered on close_time
	if len(markets) != 2 {
		t.Fatalf("got %d markets, want 2", len(markets))
	}
	m := markets[0]
	if m.VenueMarketID != "INX-26DEC31" || m.Status != "active" {
		t.Errorf("unexpected market: %+v", m)
	}
	if m.Title != "S&P closes above 6000 — on Dec 31" {
		t.Errorf("title = %q", m.Title)
	}
	if len(m.Outcomes) != 2 || m.Outcomes[0].VenueOutcomeID != "yes" || m.Outcomes[1].VenueOutcomeID != "no" {
		t.Errorf("outcomes wrong: %+v", m.Outcomes)
	}
	if m.Raw == nil {
		t.Error("raw payload not preserved")
	}
}

func TestFetchQuotes(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	a := newTestAdapter(t, srv.URL)
	quotes, err := a.FetchQuotes(context.Background(), []string{"INX-26DEC31", "EMPTYBOOK"})
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 4 {
		t.Fatalf("got %d quotes, want 4 (yes+no for two markets)", len(quotes))
	}

	byKey := map[string]ingest.Quote{}
	for _, q := range quotes {
		byKey[q.VenueMarketID+"/"+q.VenueOutcomeID] = q
	}

	yes := byKey["INX-26DEC31/yes"]
	if yes.Bid != "0.57" || yes.Ask != "0.59" || yes.Last != "0.58" {
		t.Errorf("yes side = %q/%q/%q, want 0.57/0.59/0.58", yes.Bid, yes.Ask, yes.Last)
	}
	no := byKey["INX-26DEC31/no"]
	if no.Bid != "0.41" || no.Ask != "0.43" || no.Last != "" {
		t.Errorf("no side = %q/%q/%q, want 0.41/0.43/NULL", no.Bid, no.Ask, no.Last)
	}
	if yes.Volume24h != "1200" || yes.Liquidity != "25099.23" {
		t.Errorf("stats = vol %q liq %q, want 1200 / 25099.23", yes.Volume24h, yes.Liquidity)
	}

	empty := byKey["EMPTYBOOK/yes"]
	if empty.Bid != "" || empty.Ask != "" || empty.Last != "" {
		t.Errorf("empty book must be NULLs, got %q/%q/%q", empty.Bid, empty.Ask, empty.Last)
	}
}
