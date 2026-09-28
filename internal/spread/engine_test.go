package spread

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeSource struct {
	rows []QuoteRow
	err  error
}

func (f *fakeSource) SpreadCandidates(context.Context) ([]QuoteRow, error) {
	return f.rows, f.err
}

// fakeSink models the partial unique index from migration 006: at most one
// OPEN alert per dedup_key.
type fakeSink struct {
	open     map[string]bool
	openCall int
	closed   [][]string
	openErr  error
}

func newFakeSink() *fakeSink { return &fakeSink{open: map[string]bool{}} }

func (f *fakeSink) OpenAlert(_ context.Context, key, _ string, _ *int64, _ []byte, _ string) (bool, error) {
	f.openCall++
	if f.openErr != nil {
		return false, f.openErr
	}
	if f.open[key] {
		return false, nil // conflict: already live
	}
	f.open[key] = true
	return true, nil
}

func (f *fakeSink) CloseAlertsExcept(_ context.Context, live []string) (int64, error) {
	f.closed = append(f.closed, live)
	liveSet := map[string]bool{}
	for _, k := range live {
		liveSet[k] = true
	}
	var n int64
	for k := range f.open {
		if !liveSet[k] {
			delete(f.open, k)
			n++
		}
	}
	return n, nil
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// An arb across two venues, profitable in exactly one direction.
func arbRows() []QuoteRow {
	return []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.45", "50000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "0.56", "50000"),
		row(1, 20, 200, "kalshi", kalshiFees, "yes", "0.60", "50000"),
		row(1, 20, 201, "kalshi", kalshiFees, "no", "0.50", "50000"),
	}
}

func TestScanOpensAlertForNewArb(t *testing.T) {
	sink := newFakeSink()
	e := NewEngine(&fakeSource{rows: arbRows()}, sink, quietLog())

	e.scan(context.Background())

	if len(sink.open) != 1 {
		t.Fatalf("got %d open alerts, want 1", len(sink.open))
	}
}

// The whole point of migration 006: an arb that persists across scans must
// alert once, not once per tick.
func TestScanDoesNotRealertWhileArbPersists(t *testing.T) {
	sink := newFakeSink()
	e := NewEngine(&fakeSource{rows: arbRows()}, sink, quietLog())

	for i := 0; i < 5; i++ {
		e.scan(context.Background())
	}

	if len(sink.open) != 1 {
		t.Fatalf("got %d open alerts after 5 scans, want 1", len(sink.open))
	}
	if sink.openCall != 5 {
		t.Errorf("OpenAlert called %d times, want 5 (the index does the suppressing)", sink.openCall)
	}
}

// Once the arb is gone its alert closes, so the same opportunity can alert
// again if it comes back — a second occurrence is news a second time.
func TestScanClosesAlertWhenArbDisappearsThenRealerts(t *testing.T) {
	src := &fakeSource{rows: arbRows()}
	sink := newFakeSink()
	e := NewEngine(src, sink, quietLog())

	e.scan(context.Background())
	if len(sink.open) != 1 {
		t.Fatalf("setup: got %d open alerts, want 1", len(sink.open))
	}

	// Prices converge — no edge left anywhere.
	src.rows = []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.55", "50000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "0.46", "50000"),
		row(1, 20, 200, "kalshi", kalshiFees, "yes", "0.56", "50000"),
		row(1, 20, 201, "kalshi", kalshiFees, "no", "0.45", "50000"),
	}
	e.scan(context.Background())
	if len(sink.open) != 0 {
		t.Fatalf("got %d open alerts after arb vanished, want 0", len(sink.open))
	}

	// It comes back.
	src.rows = arbRows()
	e.scan(context.Background())
	if len(sink.open) != 1 {
		t.Errorf("got %d open alerts after arb returned, want 1", len(sink.open))
	}
}

func TestScanWithNoCandidatesStillClosesStaleAlerts(t *testing.T) {
	src := &fakeSource{rows: arbRows()}
	sink := newFakeSink()
	e := NewEngine(src, sink, quietLog())

	e.scan(context.Background())

	// Every match gets un-reviewed, so nothing is a candidate any more.
	src.rows = nil
	e.scan(context.Background())

	if len(sink.open) != 0 {
		t.Errorf("got %d open alerts, want 0 — stale alerts must not linger", len(sink.open))
	}
}

// Ingestion is the thing that cannot be rebuilt: a failing scan logs and
// returns, never panics or takes the process down.
func TestScanSurvivesSourceFailure(t *testing.T) {
	sink := newFakeSink()
	e := NewEngine(&fakeSource{err: errors.New("db is down")}, sink, quietLog())

	e.scan(context.Background()) // must not panic

	if len(sink.closed) != 0 {
		t.Error("a failed read must not close alerts — that would look like every arb resolved")
	}
}

func TestScanSurvivesSinkFailure(t *testing.T) {
	sink := newFakeSink()
	sink.openErr = errors.New("insert failed")
	e := NewEngine(&fakeSource{rows: arbRows()}, sink, quietLog())

	e.scan(context.Background()) // must not panic

	if len(sink.open) != 0 {
		t.Errorf("got %d open alerts despite insert failure", len(sink.open))
	}
}

// Both directions are separately keyed, so one cannot suppress the other.
func TestDedupKeyDistinguishesDirection(t *testing.T) {
	yesPoly := Result{
		YesLeg: Leg{VenueSlug: "polymarket"},
		NoLeg:  Leg{VenueSlug: "kalshi"},
	}
	yesKalshi := Result{
		YesLeg: Leg{VenueSlug: "kalshi"},
		NoLeg:  Leg{VenueSlug: "polymarket"},
	}

	if a, b := dedupKey(1, yesPoly), dedupKey(1, yesKalshi); a == b {
		t.Errorf("both directions share dedup key %q; one would suppress the other", a)
	}
}

func TestDedupKeyDistinguishesMatch(t *testing.T) {
	r := Result{YesLeg: Leg{VenueSlug: "polymarket"}, NoLeg: Leg{VenueSlug: "kalshi"}}
	if a, b := dedupKey(1, r), dedupKey(2, r); a == b {
		t.Errorf("different matches share dedup key %q", a)
	}
}
