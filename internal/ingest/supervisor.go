package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Sink is where normalized venue data lands. The store implements it; the
// supervisor never touches SQL directly.
type Sink interface {
	// UpsertMarkets persists markets + outcomes idempotently and returns
	// how many markets were written.
	UpsertMarkets(ctx context.Context, venueSlug string, markets []Market) (int, error)
	// InsertQuotes persists quote snapshots (duplicates are no-ops) and
	// returns how many rows were actually inserted.
	InsertQuotes(ctx context.Context, venueSlug string, quotes []Quote) (int, error)
	// ActiveMarketIDs returns the venue_market_ids currently worth
	// quoting: status=active with close_time in the future or unknown.
	ActiveMarketIDs(ctx context.Context, venueSlug string) ([]string, error)
}

// VenueStatus is the supervisor's in-memory view of one venue loop,
// exposed to /healthz alongside the DB-derived quote lag.
type VenueStatus struct {
	Slug       string
	Running    bool
	LastErr    string
	LastPollAt time.Time
	Restarts   int
}

// VenueSpec binds an adapter to its polling cadence.
type VenueSpec struct {
	Adapter    Adapter
	MetaEvery  time.Duration // market metadata refresh (default 60s)
	QuoteEvery time.Duration // top-of-book sweep (default 10s)
}

// Supervisor runs one goroutine per venue with panic recovery and
// exponential-backoff restarts.
//
// WHY supervised goroutines instead of separate processes: the
// architecture mandates one binary, but venue isolation is a hard rule —
// a panic or persistent API failure in one venue's loop must never affect
// another venue. Each loop is crash-contained here and restarted with
// backoff, exactly like a tiny in-process systemd.
type Supervisor struct {
	sink   Sink
	log    *slog.Logger
	venues []VenueSpec

	// backoff knobs, overridable in tests
	backoffBase time.Duration
	backoffMax  time.Duration
	// a run longer than this resets the backoff counter
	healthyAfter time.Duration

	mu     sync.Mutex
	status map[string]*VenueStatus
}

func NewSupervisor(sink Sink, log *slog.Logger, venues []VenueSpec) *Supervisor {
	s := &Supervisor{
		sink:         sink,
		log:          log,
		venues:       venues,
		backoffBase:  time.Second,
		backoffMax:   5 * time.Minute,
		healthyAfter: 10 * time.Minute,
		status:       make(map[string]*VenueStatus),
	}
	for _, v := range venues {
		s.status[v.Adapter.Slug()] = &VenueStatus{Slug: v.Adapter.Slug()}
	}
	return s
}

// Run blocks until ctx is cancelled, supervising every venue loop.
func (s *Supervisor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, v := range s.venues {
		wg.Add(1)
		go func(v VenueSpec) {
			defer wg.Done()
			s.superviseVenue(ctx, v)
		}(v)
	}
	wg.Wait()
}

// Status returns a snapshot of every venue's loop state.
func (s *Supervisor) Status() map[string]VenueStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]VenueStatus, len(s.status))
	for k, v := range s.status {
		out[k] = *v
	}
	return out
}

func (s *Supervisor) superviseVenue(ctx context.Context, v VenueSpec) {
	slug := v.Adapter.Slug()
	log := s.log.With("venue", slug)
	attempt := 0

	for ctx.Err() == nil {
		s.setRunning(slug, true)
		started := time.Now()
		err := s.runVenueOnce(ctx, v, log)
		s.setRunning(slug, false)
		if ctx.Err() != nil {
			return
		}

		// WHY reset after a healthy stretch: a venue that ran fine for an
		// hour and then hiccuped should retry in 1s, not wherever the
		// counter was left weeks ago.
		if time.Since(started) > s.healthyAfter {
			attempt = 0
		}
		wait := Backoff(attempt, s.backoffBase, s.backoffMax)
		attempt++
		s.recordErr(slug, err)
		log.Error("venue loop crashed; restarting",
			"err", err, "restart_in", wait.String(), "attempt", attempt)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runVenueOnce runs one venue's polling loop until it errors, panics, or
// the context is cancelled. Any error escapes to the supervisor, which
// restarts the loop with backoff — that single mechanism covers both
// "HTTP errors back off (max 5 min)" and crash recovery.
func (s *Supervisor) runVenueOnce(ctx context.Context, v VenueSpec, log *slog.Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()

	slug := v.Adapter.Slug()

	// Metadata first: quotes are meaningless until markets exist.
	marketCount, err := s.pollMeta(ctx, v.Adapter)
	if err != nil {
		return fmt.Errorf("metadata poll: %w", err)
	}
	quoteCount, err := s.pollQuotes(ctx, v.Adapter)
	if err != nil {
		return fmt.Errorf("quote poll: %w", err)
	}
	log.Info("poll cycle", "markets", marketCount, "quotes", quoteCount)
	s.touch(slug)

	metaTick := time.NewTicker(v.MetaEvery)
	defer metaTick.Stop()
	quoteTick := time.NewTicker(v.QuoteEvery)
	defer quoteTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-metaTick.C:
			n, err := s.pollMeta(ctx, v.Adapter)
			if err != nil {
				return fmt.Errorf("metadata poll: %w", err)
			}
			marketCount = n
			s.touch(slug)
		case <-quoteTick.C:
			n, err := s.pollQuotes(ctx, v.Adapter)
			if err != nil {
				return fmt.Errorf("quote poll: %w", err)
			}
			quoteCount = n
			log.Info("poll cycle", "markets", marketCount, "quotes", quoteCount)
			s.touch(slug)
		}
	}
}

func (s *Supervisor) pollMeta(ctx context.Context, a Adapter) (int, error) {
	markets, err := a.FetchMarkets(ctx)
	if err != nil {
		return 0, err
	}
	return s.sink.UpsertMarkets(ctx, a.Slug(), markets)
}

func (s *Supervisor) pollQuotes(ctx context.Context, a Adapter) (int, error) {
	ids, err := s.sink.ActiveMarketIDs(ctx, a.Slug())
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	quotes, err := a.FetchQuotes(ctx, ids)
	if err != nil {
		return 0, err
	}
	return s.sink.InsertQuotes(ctx, a.Slug(), quotes)
}

func (s *Supervisor) setRunning(slug string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[slug]
	st.Running = running
	if running {
		st.LastErr = ""
	}
}

func (s *Supervisor) recordErr(slug string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[slug]
	st.Restarts++
	if err != nil {
		st.LastErr = err.Error()
	}
}

func (s *Supervisor) touch(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[slug].LastPollAt = time.Now()
}
