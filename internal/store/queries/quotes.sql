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

-- name: LatestQuotePerOutcome :many
-- Newest stored price per outcome, with the venue/market/outcome identifiers
-- the write path keys on. Used once at startup to warm the in-memory dedup
-- cache so a restart does not re-write a duplicate row for every outcome on
-- the first poll. ::text yields the canonical NUMERIC text (NULL stays NULL).
SELECT DISTINCT ON (q.outcome_id)
       v.slug             AS venue_slug,
       m.venue_market_id  AS venue_market_id,
       o.venue_outcome_id AS venue_outcome_id,
       COALESCE(q.bid::text, '')::text  AS bid,
       COALESCE(q.ask::text, '')::text  AS ask,
       COALESCE(q.last::text, '')::text AS last
FROM quotes q
JOIN outcomes o ON o.id = q.outcome_id
JOIN markets m  ON m.id = o.market_id
JOIN venues v   ON v.id = m.venue_id
ORDER BY q.outcome_id, q.time DESC;
