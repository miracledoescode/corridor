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
	// InsertQuotes persists quote snapshots and returns how many rows were
	// actually written. Unchanged prices are deduped away, so the return is
	// "rows written", which is <= len(quotes).
	InsertQuotes(ctx context.Context, venueSlug string, quotes []Quote) (int, error)
	// ActiveMarketIDs returns the venue_market_ids currently worth
	// quoting: status=active with close_time in the future or unknown.
	ActiveMarketIDs(ctx context.Context, venueSlug string) ([]string, error)
	// MarkVenuePolled stamps the venue's liveness heartbeat. Called once per
	// quote cycle so health can distinguish a stable-price venue (no new
	// quotes, still healthy) from a dead one.
	MarkVenuePolled(ctx context.Context, venueSlug string) error
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
//
// WHY two goroutines per venue (meta + quotes) rather than one: the metadata
// refresh writes hundreds of rows to a remote DB and can take tens of seconds;
// running it in the same loop as the 10s quote sweep froze quote ingestion for
// that whole stretch. Splitting them means a slow metadata write never blocks
// price capture (or the liveness heartbeat). Venue isolation is preserved —
// each loop is independently crash-contained and backoff-restarted, and one
// venue's loops can never affect another's. The adapter is safe for concurrent
// FetchMarkets/FetchQuotes, and both loops share the venue's 1 req/s limiter,
// so politeness is unchanged.
func (s *Supervisor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, v := range s.venues {
		wg.Add(2)
		// quotes is the "primary" loop: it owns the venue's Running status
		// (health cares about price capture, not metadata freshness).
		go func(v VenueSpec) {
			defer wg.Done()
			s.superviseTask(ctx, v, "quote", true, s.runQuotes)
		}(v)
		go func(v VenueSpec) {
			defer wg.Done()
			s.superviseTask(ctx, v, "meta", false, s.runMeta)
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

// superviseTask runs one loop (meta or quotes) for a venue with panic
// recovery and exponential-backoff restarts. The primary loop owns the
// venue's Running status. Any error escapes here and is restarted with
// backoff — one mechanism for both "HTTP errors back off" and crash recovery.
func (s *Supervisor) superviseTask(ctx context.Context, v VenueSpec, name string, primary bool,
	run func(context.Context, VenueSpec, *slog.Logger) error) {
	slug := v.Adapter.Slug()
	log := s.log.With("venue", slug, "loop", name)
	attempt := 0

	for ctx.Err() == nil {
		if primary {
			s.setRunning(slug, true)
		}
		started := time.Now()
		err := run(ctx, v, log)
		if primary {
			s.setRunning(slug, false)
		}
		if ctx.Err() != nil {
			return
		}

		// WHY reset after a healthy stretch: a loop that ran fine for an
		// hour and then hiccuped should retry in 1s, not wherever the
		// counter was left.
		if time.Since(started) > s.healthyAfter {
			attempt = 0
		}
		wait := Backoff(attempt, s.backoffBase, s.backoffMax)
		attempt++
		s.recordErr(slug, err)
		log.Error("loop crashed; restarting",
			"err", err, "restart_in", wait.String(), "attempt", attempt)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runMeta refreshes market metadata on MetaEvery until it errors, panics, or
// ctx is cancelled. It runs independently of the quote loop so a slow
// metadata write never blocks price capture.
func (s *Supervisor) runMeta(ctx context.Context, v VenueSpec, log *slog.Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()

	refresh := func() error {
		n, err := s.pollMeta(ctx, v.Adapter)
		if err != nil {
			return fmt.Errorf("metadata poll: %w", err)
		}
		log.Info("metadata refreshed", "markets", n)
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}

	t := time.NewTicker(v.MetaEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := refresh(); err != nil {
				return err
			}
		}
	}
}

// runQuotes sweeps top-of-book on QuoteEvery until it errors, panics, or ctx
// is cancelled. polled = outcomes fetched; wrote = rows persisted after
// dedup. A healthy quiet market shows polled>0, wrote==0 — dedup working, not
// a stall. The heartbeat (inside pollQuotes) advances every cycle regardless.
func (s *Supervisor) runQuotes(ctx context.Context, v VenueSpec, log *slog.Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()

	slug := v.Adapter.Slug()
	sweep := func() error {
		polled, wrote, err := s.pollQuotes(ctx, v.Adapter)
		if err != nil {
			return fmt.Errorf("quote poll: %w", err)
		}
		log.Info("poll cycle", "polled", polled, "wrote", wrote)
		s.touch(slug)
		return nil
	}
	if err := sweep(); err != nil {
		return err
	}

	t := time.NewTicker(v.QuoteEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := sweep(); err != nil {
				return err
			}
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

// pollQuotes runs one quote cycle and returns (polled, wrote): outcomes
// fetched vs rows actually persisted after dedup. It stamps the venue
// heartbeat on every successful cycle — including the no-active-markets case —
// so a quiet but live venue never reads as down.
func (s *Supervisor) pollQuotes(ctx context.Context, a Adapter) (polled int, wrote int, err error) {
	ids, err := s.sink.ActiveMarketIDs(ctx, a.Slug())
	if err != nil {
		return 0, 0, err
	}
	if len(ids) == 0 {
		s.markPolled(ctx, a.Slug())
		return 0, 0, nil
	}
	quotes, err := a.FetchQuotes(ctx, ids)
	if err != nil {
		return 0, 0, err
	}
	wrote, err = s.sink.InsertQuotes(ctx, a.Slug(), quotes)
	if err != nil {
		return len(quotes), wrote, err
	}
	s.markPolled(ctx, a.Slug())
	return len(quotes), wrote, nil
}

// markPolled stamps the liveness heartbeat best-effort. WHY swallow the error:
// the quote write already succeeded; failing the whole cycle (and triggering a
// backoff restart) because a one-row heartbeat update hiccuped would hurt
// ingestion, not help it. A missed heartbeat just makes /healthz briefly
// pessimistic, which the next cycle corrects.
func (s *Supervisor) markPolled(ctx context.Context, slug string) {
	if err := s.sink.MarkVenuePolled(ctx, slug); err != nil {
		s.log.Warn("venue heartbeat failed", "venue", slug, "err", err)
	}
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
