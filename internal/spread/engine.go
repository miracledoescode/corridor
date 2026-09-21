package spread

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Sink records detected opportunities.
type Sink interface {
	// OpenAlert records a new opportunity, reporting false if one is already
	// open for this key.
	OpenAlert(ctx context.Context, dedupKey, kind string, eventID *int64, payload []byte, netEdge string) (bool, error)

	// CloseAlertsExcept closes open alerts whose opportunity is gone.
	CloseAlertsExcept(ctx context.Context, liveKeys []string) (int64, error)
}

// AlertKind is written to alerts.kind.
const AlertKind = "cross_venue_arb"

// Engine scans matched markets on a timer and records arbitrage it finds.
type Engine struct {
	src  Source
	sink Sink
	log  *slog.Logger
}

func NewEngine(src Source, sink Sink, log *slog.Logger) *Engine {
	return &Engine{src: src, sink: sink, log: log}
}

// Run scans every interval until ctx is cancelled.
//
// WHY every failure is logged and never fatal: this runs as a goroutine
// inside corridord alongside the ingestion supervisor. A spread scan that
// cannot reach the database, or trips over one malformed fee model, must
// never take the process down with it — ingestion is the thing that cannot
// be rebuilt, and a missed scan costs one interval.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	e.scan(ctx) // once at startup, so a restart does not wait a full interval

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.scan(ctx)
		}
	}
}

func (e *Engine) scan(ctx context.Context) {
	rows, err := e.src.SpreadCandidates(ctx)
	if err != nil {
		e.log.Error("spread scan failed", "err", err)
		return
	}
	if len(rows) == 0 {
		// Nothing matched and human-reviewed yet. Still close anything left
		// open, so stale alerts do not linger after a match is un-reviewed.
		e.closeStale(ctx, nil)
		return
	}

	pairs, problems := Assemble(rows)
	for _, p := range problems {
		// Unpriceable pairs are expected while fee models or matches are
		// incomplete. Log rather than fail: one bad pair must not stop the
		// others from being priced.
		e.log.Warn("skipping unpriceable pair", "err", p)
	}

	var liveKeys []string
	opened := 0

	for _, pair := range pairs {
		for _, opp := range pair.Opportunities() {
			key := dedupKey(pair.MatchID, opp)
			liveKeys = append(liveKeys, key)

			payload, err := json.Marshal(newPayload(pair, opp))
			if err != nil {
				e.log.Error("marshal alert payload", "err", err, "match_id", pair.MatchID)
				continue
			}

			isNew, err := e.sink.OpenAlert(
				ctx, key, AlertKind, pair.EventID, payload, opp.NetEdge.FloatString(6),
			)
			if err != nil {
				e.log.Error("open alert", "err", err, "dedup_key", key)
				continue
			}
			if isNew {
				opened++
				e.log.Info("arb detected",
					"match_id", pair.MatchID,
					"net_edge", opp.NetEdge.FloatString(4),
					"yes_venue", opp.YesLeg.VenueSlug,
					"no_venue", opp.NoLeg.VenueSlug,
				)
			}
		}
	}

	e.closeStale(ctx, liveKeys)

	if opened > 0 {
		e.log.Info("spread scan complete", "pairs", len(pairs), "new_alerts", opened)
	}
}

func (e *Engine) closeStale(ctx context.Context, liveKeys []string) {
	closed, err := e.sink.CloseAlertsExcept(ctx, liveKeys)
	if err != nil {
		e.log.Error("close stale alerts", "err", err)
		return
	}
	if closed > 0 {
		e.log.Info("closed resolved alerts", "count", closed)
	}
}

// dedupKey identifies one opportunity: a matched pair AND a direction.
//
// WHY the direction is part of the key: YES on venue A with NO on venue B is
// a different trade from its mirror, with a different price and a different
// edge. Keying on the match alone would let the first direction found
// suppress the second.
func dedupKey(matchID int64, r Result) string {
	return fmt.Sprintf("%d:yes@%s:no@%s", matchID, r.YesLeg.VenueSlug, r.NoLeg.VenueSlug)
}

// payload is the alerts.payload JSONB document — everything needed to render
// a notification without re-querying.
//
// WHY there is no size field: quotes carries no size-at-ask, and the venue
// liquidity figure is a market-level dollar aggregate rather than depth at
// this price (see Leg.LiquidityUSD). Any size derived from it is an upper
// bound, and publishing an upper bound as a fillable quantity would break
// "never advertise an arb bigger than its book". Stating the edge and saying
// nothing about size is the honest version of this alert.
type payload struct {
	MatchID    int64      `json:"match_id"`
	Title      string     `json:"title"`
	GrossEdge  string     `json:"gross_edge"`
	NetEdge    string     `json:"net_edge"`
	Yes        payloadLeg `json:"yes"`
	No         payloadLeg `json:"no"`
	DetectedAt time.Time  `json:"detected_at"`
}

type payloadLeg struct {
	Venue    string `json:"venue"`
	MarketID int64  `json:"market_id"`
	Label    string `json:"label"`
	Ask      string `json:"ask"`
}

func newPayload(p Pair, r Result) payload {
	return payload{
		MatchID:   p.MatchID,
		Title:     p.Sides[0].Title,
		GrossEdge: r.GrossEdge.FloatString(6),
		NetEdge:   r.NetEdge.FloatString(6),
		Yes: payloadLeg{
			Venue:    r.YesLeg.VenueSlug,
			MarketID: r.YesLeg.MarketID,
			Label:    r.YesLeg.Label,
			Ask:      r.YesLeg.Ask.FloatString(4),
		},
		No: payloadLeg{
			Venue:    r.NoLeg.VenueSlug,
			MarketID: r.NoLeg.MarketID,
			Label:    r.NoLeg.Label,
			Ask:      r.NoLeg.Ask.FloatString(4),
		},
		DetectedAt: time.Now().UTC(),
	}
}
