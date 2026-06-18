-- name: GetVenueIDBySlug :one
SELECT id FROM venues WHERE slug = $1;

-- name: UpsertMarketBatch :batchexec
-- Batched upsert: one round trip for the whole metadata sweep instead of a
-- BEGIN/INSERT/COMMIT per market. WHY this matters: corridord talks to a
-- REMOTE Supabase, so per-market transactions meant ~4 network round trips ×
-- hundreds of markets ≈ tens of seconds of latency every cycle, which froze
-- the venue's quote loop. pgx sends a batch in one round trip (and under the
-- pool's simple-protocol mode, as one combined query — pooler-safe).
INSERT INTO markets (venue_id, venue_market_id, title, resolution_criteria, close_time, status, raw)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (venue_id, venue_market_id) DO UPDATE SET
    title               = EXCLUDED.title,
    resolution_criteria = EXCLUDED.resolution_criteria,
    close_time          = EXCLUDED.close_time,
    status              = EXCLUDED.status,
    raw                 = EXCLUDED.raw,
    last_seen           = now();

-- name: UpsertOutcomeBatch :batchexec
-- Resolves market_id inline from (venue_id, venue_market_id) so outcomes need
-- no surrogate id handed back from the market upsert — the whole metadata
-- write becomes two batches (markets, then outcomes) in one transaction.
INSERT INTO outcomes (market_id, venue_outcome_id, label)
SELECT m.id, sqlc.arg(venue_outcome_id), sqlc.arg(label)
FROM markets m
WHERE m.venue_id = sqlc.arg(venue_id)
  AND m.venue_market_id = sqlc.arg(venue_market_id)
ON CONFLICT (market_id, venue_outcome_id) DO UPDATE SET
    label = EXCLUDED.label;

-- name: ActiveMarketIDs :many
SELECT m.venue_market_id
FROM markets m
JOIN venues v ON v.id = m.venue_id
WHERE v.slug = $1
  AND m.status = 'active'
  AND (m.close_time IS NULL OR m.close_time > now())
ORDER BY m.id;
