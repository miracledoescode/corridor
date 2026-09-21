package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/miracledoescode/corridor/internal/notify"
	"github.com/miracledoescode/corridor/internal/spread"
)

// spreadCandidatesSQL returns the latest quote for every outcome of every
// market in an EXACT, human-reviewed match.
//
// WHY both conditions and not just EXACT: "Arb alerts fire ONLY on
// confidence = EXACT matches" is the hard rule, and the matcher's documented
// safety property is that a human reviews before anything is treated as
// tradable. reviewed_by_human is what records that review, so requiring both
// is the conservative reading — an unreviewed EXACT is the LLM's opinion, not
// a verified one, and a false EXACT is precisely what would advertise a fake
// arbitrage.
//
// WHY ::text on the numerics: the never-float hard rule. Exact decimal text
// goes straight into big.Rat in the spread package with nothing float-shaped
// in between.
//
// WHY the LATERAL: it asks for one row per outcome — the most recent quote —
// and quotes_outcome_time_idx is exactly (outcome_id, time DESC), so each
// lookup is an index seek rather than a scan of that outcome's history.
const spreadCandidatesSQL = `
SELECT
    mm.id,
    m.event_id,
    m.id,
    m.title,
    v.slug,
    v.fee_model,
    o.id,
    o.label,
    COALESCE(q.ask::text, ''),
    COALESCE(q.liquidity::text, '')
FROM market_matches mm
JOIN markets  m ON m.id IN (mm.market_a, mm.market_b)
JOIN venues   v ON v.id = m.venue_id
JOIN outcomes o ON o.market_id = m.id
LEFT JOIN LATERAL (
    SELECT ask, liquidity
    FROM quotes
    WHERE outcome_id = o.id
    ORDER BY time DESC
    LIMIT 1
) q ON true
WHERE mm.confidence = 'EXACT'
  AND mm.reviewed_by_human = true
  AND m.status = 'active'
ORDER BY mm.id, m.id, o.id
`

// SpreadCandidates implements spread.Source.
func (s *Store) SpreadCandidates(ctx context.Context) ([]spread.QuoteRow, error) {
	rows, err := s.pool.Query(ctx, spreadCandidatesSQL)
	if err != nil {
		return nil, fmt.Errorf("spread candidates: %w", err)
	}
	defer rows.Close()

	var out []spread.QuoteRow
	for rows.Next() {
		var r spread.QuoteRow
		if err := rows.Scan(
			&r.MatchID,
			&r.EventID,
			&r.MarketID,
			&r.Title,
			&r.VenueSlug,
			&r.FeeModel,
			&r.OutcomeID,
			&r.Label,
			&r.AskText,
			&r.LiquidityText,
		); err != nil {
			return nil, fmt.Errorf("scan spread candidate: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OpenAlert records a newly-detected opportunity and reports whether it was
// actually new.
//
// The partial unique index from migration 006 allows at most one OPEN alert
// per dedup_key, so ON CONFLICT DO NOTHING makes this idempotent by
// construction: a repeated engine tick, two ticks racing, or a restart
// mid-scan cannot produce a second alert for an arb that is already live.
// A false return means "already alerted, still live" — not an error.
func (s *Store) OpenAlert(
	ctx context.Context,
	dedupKey, kind string,
	eventID *int64,
	payload []byte,
	netEdge string,
) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
        INSERT INTO alerts (kind, event_id, payload, net_edge, dedup_key)
        VALUES ($1, $2, $3, $4::numeric, $5)
        ON CONFLICT (dedup_key) WHERE closed_at IS NULL AND dedup_key IS NOT NULL
        DO NOTHING
    `, kind, eventID, payload, netEdge, dedupKey)
	if err != nil {
		return false, fmt.Errorf("open alert %s: %w", dedupKey, err)
	}
	return tag.RowsAffected() > 0, nil
}

// CloseAlertsExcept closes every open alert whose opportunity is no longer
// live, and returns how many it closed.
//
// An empty liveKeys correctly closes everything: no arbs are live, so no
// alert should be. Closing is what lets the same opportunity alert again if
// it reappears later — a second occurrence is news a second time.
func (s *Store) CloseAlertsExcept(ctx context.Context, liveKeys []string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
        UPDATE alerts
        SET closed_at = now()
        WHERE closed_at IS NULL
          AND dedup_key IS NOT NULL
          AND NOT (dedup_key = ANY($1))
    `, liveKeys)
	if err != nil {
		return 0, fmt.Errorf("close stale alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ClaimUndispatchedAlerts stamps dispatched_at on pending alerts and returns
// them, implementing notify.Store.
//
// WHY one UPDATE ... RETURNING rather than SELECT-then-UPDATE: the stamp and
// the read must be a single atomic step. A SELECT followed by an UPDATE lets
// two dispatcher ticks (or two processes) read the same pending alert and
// send it twice. Doing it in one statement means a row is claimed exactly
// once — the row lock is held for the whole operation.
//
// WHY FOR UPDATE SKIP LOCKED: a concurrent claim takes the next available
// rows instead of blocking on the ones already being claimed.
//
// WHY oldest-first: alerts are time-critical, so a backlog drains in the
// order the opportunities were actually detected.
func (s *Store) ClaimUndispatchedAlerts(ctx context.Context, limit int) ([]notify.Alert, error) {
	rows, err := s.pool.Query(ctx, `
        UPDATE alerts
        SET dispatched_at = now()
        WHERE id IN (
            SELECT id FROM alerts
            WHERE dispatched_at IS NULL
            ORDER BY created_at
            LIMIT $1
            FOR UPDATE SKIP LOCKED
        )
        RETURNING id, kind, payload
    `, limit)
	if err != nil {
		return nil, fmt.Errorf("claim alerts: %w", err)
	}
	defer rows.Close()

	var out []notify.Alert
	for rows.Next() {
		var a notify.Alert
		var raw []byte
		if err := rows.Scan(&a.ID, &a.Kind, &raw); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		if err := json.Unmarshal(raw, &a.Payload); err != nil {
			return nil, fmt.Errorf("decode alert %d payload: %w", a.ID, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
