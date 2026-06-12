-- name: InsertQuote :execrows
-- Resolves the outcome's surrogate id inline so the ingest path never has
-- to maintain (and invalidate) an id cache.
-- WHY bare ON CONFLICT DO NOTHING: the dedupe index is declared with
-- "time DESC", and Postgres conflict-target inference does not match
-- unique indexes with a non-default sort direction — the bare form still
-- catches the violation.
INSERT INTO quotes (time, outcome_id, bid, ask, last, volume_24h, liquidity, raw)
SELECT sqlc.arg('time')::timestamptz, o.id,
       sqlc.arg(bid), sqlc.arg(ask), sqlc.arg(last),
       sqlc.arg(volume_24h), sqlc.arg(liquidity), sqlc.arg(raw)
FROM outcomes o
JOIN markets m ON m.id = o.market_id
JOIN venues v  ON v.id = m.venue_id
WHERE v.slug = sqlc.arg(venue_slug)
  AND m.venue_market_id = sqlc.arg(venue_market_id)
  AND o.venue_outcome_id = sqlc.arg(venue_outcome_id)
ON CONFLICT DO NOTHING;
