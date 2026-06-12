package ingest

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAdapter lets tests script venue behavior, including panics.
type fakeAdapter struct {
	slug       string
	fetchCalls atomic.Int64
	panicUntil int64 // FetchMarkets panics while fetchCalls <= panicUntil
}

func (f *fakeAdapter) FetchMarkets(ctx context.Context) ([]Market, error) {
	n := f.fetchCalls.Add(1)
	if n <= f.panicUntil {
		panic("scripted venue panic")
	}
	return []Market{{VenueMarketID: "m1", Title: "t", Status: "active", Raw: []byte(`{}`)}}, nil
}

func (f *fakeAdapter) FetchQuotes(ctx context.Context, ids []string) ([]Quote, error) {
	return []Quote{{VenueMarketID: "m1", VenueOutcomeID: "o1", Time: time.Now()}}, nil
}

func (f *fakeAdapter) Health(ctx context.Context) error { return nil }
func (f *fakeAdapter) Slug() string                     { return f.slug }

// memSink counts writes per venue.
type memSink struct {
	mu      sync.Mutex
	markets map[string]int
	quotes  map[string]int
}

func newMemSink() *memSink {
	return &memSink{markets: map[string]int{}, quotes: map[string]int{}}
}

func (m *memSink) UpsertMarkets(ctx context.Context, slug string, ms []Market) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markets[slug] += len(ms)
	return len(ms), nil
}

func (m *memSink) InsertQuotes(ctx context.Context, slug string, qs []Quote) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotes[slug] += len(qs)
	return len(qs), nil
}

func (m *memSink) ActiveMarketIDs(ctx context.Context, slug string) ([]string, error) {
	return []string{"m1"}, nil
}

func (m *memSink) quoteCount(slug string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quotes[slug]
}

// TestVenueIsolation is the hard rule: one venue panicking must never
// stop another venue's ingestion.
func TestVenueIsolation(t *testing.T) {
	sink := newMemSink()
	bad := &fakeAdapter{slug: "badvenue", panicUntil: 3}
	good := &fakeAdapter{slug: "goodvenue"}

	s := NewSupervisor(sink, slog.Default(), []VenueSpec{
		{Adapter: bad, MetaEvery: 10 * time.Millisecond, QuoteEvery: 5 * time.Millisecond},
		{Adapter: good, MetaEvery: 10 * time.Millisecond, QuoteEvery: 5 * time.Millisecond},
	})
	s.backoffBase = time.Millisecond
	s.backoffMax = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if got := sink.quoteCount("goodvenue"); got == 0 {
		t.Fatal("good venue ingested zero quotes while bad venue was panicking")
	}
	if bad.fetchCalls.Load() <= 3 {
		t.Fatalf("bad venue was never restarted after panics: %d calls", bad.fetchCalls.Load())
	}
	if got := sink.quoteCount("badvenue"); got == 0 {
		t.Fatal("bad venue never recovered after panics stopped")
	}

	st := s.Status()
	if st["badvenue"].Restarts < 3 {
		t.Errorf("expected >=3 recorded restarts for badvenue, got %d", st["badvenue"].Restarts)
	}
}
