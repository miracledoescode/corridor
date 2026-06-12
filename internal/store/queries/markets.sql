-- name: GetVenueIDBySlug :one
SELECT id FROM venues WHERE slug = $1;

-- name: UpsertMarket :one
INSERT INTO markets (venue_id, venue_market_id, title, resolution_criteria, close_time, status, raw)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (venue_id, venue_market_id) DO UPDATE SET
    title               = EXCLUDED.title,
    resolution_criteria = EXCLUDED.resolution_criteria,
    close_time          = EXCLUDED.close_time,
    status              = EXCLUDED.status,
    raw                 = EXCLUDED.raw,
    last_seen           = now()
RETURNING id;

-- name: UpsertOutcome :one
INSERT INTO outcomes (market_id, venue_outcome_id, label)
VALUES ($1, $2, $3)
ON CONFLICT (market_id, venue_outcome_id) DO UPDATE SET
    label = EXCLUDED.label
RETURNING id;

-- name: ActiveMarketIDs :many
SELECT m.venue_market_id
FROM markets m
JOIN venues v ON v.id = m.venue_id
WHERE v.slug = $1
  AND m.status = 'active'
  AND (m.close_time IS NULL OR m.close_time > now())
ORDER BY m.id;
