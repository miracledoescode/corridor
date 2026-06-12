package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miracledoescode/corridor/internal/ingest"
)

func futureDate() string { return time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339) }

func gammaFixture(future, past string) string {
	return fmt.Sprintf(`[
	  {"id":"101","question":"Will X win?","description":"Resolves YES if X wins.",
	   "endDate":"%s","active":true,"closed":false,
	   "outcomes":"[\"Yes\",\"No\"]","clobTokenIds":"[\"tok-yes\",\"tok-no\"]",
	   "volume24hr":12345.5,"liquidityNum":"6789.25"},
	  {"id":"102","question":"Closed market","description":"",
	   "endDate":"%s","active":true,"closed":true,
	   "outcomes":"[\"Yes\",\"No\"]","clobTokenIds":"[\"a\",\"b\"]"},
	  {"id":"103","question":"Expired market","description":"",
	   "endDate":"%s","active":true,"closed":false,
	   "outcomes":"[\"Yes\",\"No\"]","clobTokenIds":"[\"c\",\"d\"]"},
	  {"id":"104","question":"Mismatched outcomes","description":"",
	   "endDate":"%s","active":true,"closed":false,
	   "outcomes":"[\"Yes\",\"No\"]","clobTokenIds":"[\"only-one\"]"}
	]`, future, future, past, future)
}

func newTestAdapter(t *testing.T, gamma, clob string) *Adapter {
	t.Helper()
	a := New(gamma, clob, "test-agent", slog.Default())
	// the 1 req/s production cap would make tests crawl
	a.client = ingest.NewClient("test-agent", 10000)
	return a
}

func TestFetchMarkets(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") != "0" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, gammaFixture(futureDate(), past))
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv.URL, srv.URL)
	markets, err := a.FetchMarkets(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// of the 4 fixtures only 101 survives: 102 closed, 103 expired,
	// 104 has an outcome/token mismatch
	if len(markets) != 1 {
		t.Fatalf("got %d markets, want 1", len(markets))
	}
	m := markets[0]
	if m.VenueMarketID != "101" || m.Status != "active" {
		t.Errorf("unexpected market: %+v", m)
	}
	if len(m.Outcomes) != 2 || m.Outcomes[0].VenueOutcomeID != "tok-yes" || m.Outcomes[1].Label != "No" {
		t.Errorf("outcome pairing wrong: %+v", m.Outcomes)
	}
	if m.Raw == nil {
		t.Error("raw payload not preserved")
	}
}

func TestFetchQuotes(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	mux := http.NewServeMux()
	mux.HandleFunc("/markets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") != "0" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, gammaFixture(futureDate(), past))
	})
	mux.HandleFunc("/books", func(w http.ResponseWriter, r *http.Request) {
		var req []map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("books request not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		// bids and asks deliberately unsorted: best must be computed
		fmt.Fprint(w, `[
		  {"asset_id":"tok-yes",
		   "bids":[{"price":"0.50","size":"10"},{"price":"0.57","size":"5"},{"price":"0.31","size":"9"}],
		   "asks":[{"price":"0.62","size":"4"},{"price":"0.59","size":"2"}]},
		  {"asset_id":"tok-no",
		   "bids":[{"price":"0.40","size":"1"}],
		   "asks":[]},
		  {"asset_id":"unknown-token","bids":[],"asks":[]}
		]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := newTestAdapter(t, srv.URL, srv.URL)
	if _, err := a.FetchMarkets(context.Background()); err != nil {
		t.Fatal(err)
	}
	quotes, err := a.FetchQuotes(context.Background(), []string{"101"})
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 {
		t.Fatalf("got %d quotes, want 2 (unknown token must be dropped)", len(quotes))
	}

	byOutcome := map[string]ingest.Quote{}
	for _, q := range quotes {
		byOutcome[q.VenueOutcomeID] = q
	}
	yes := byOutcome["tok-yes"]
	if yes.Bid != "0.57" || yes.Ask != "0.59" {
		t.Errorf("best-of-book wrong: bid=%q ask=%q, want 0.57/0.59", yes.Bid, yes.Ask)
	}
	if yes.VenueMarketID != "101" {
		t.Errorf("quote mapped to market %q, want 101", yes.VenueMarketID)
	}
	if yes.Volume24h != "12345.5" || yes.Liquidity != "6789.25" {
		t.Errorf("market stats not attached: vol=%q liq=%q", yes.Volume24h, yes.Liquidity)
	}
	no := byOutcome["tok-no"]
	if no.Bid != "0.40" || no.Ask != "" {
		t.Errorf("empty ask side must be NULL: bid=%q ask=%q", no.Bid, no.Ask)
	}
}
