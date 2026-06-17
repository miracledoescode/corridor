-- name: VenueLag :many
-- last_polled_at is the liveness signal: the quote loop stamps it every
-- cycle, so it advances even when prices are stable and no quote row is
-- written. last_quote_at (MAX(q.time)) is the last actual PRICE CHANGE and
-- may legitimately be old — it is informational, not a health signal.
SELECT v.slug,
       v.last_polled_at::timestamptz AS last_polled_at,
       MAX(q.time)::timestamptz      AS last_quote_at
FROM venues v
LEFT JOIN markets m  ON m.venue_id  = v.id
LEFT JOIN outcomes o ON o.market_id = m.id
LEFT JOIN quotes q   ON q.outcome_id = o.id
GROUP BY v.slug, v.last_polled_at
ORDER BY v.slug;

-- name: MarkVenuePolled :exec
-- Stamp the venue heartbeat at the end of every quote cycle. One in-place
-- update of one row per venue per cycle — no storage growth, and it keeps
-- health honest once quote writes are deduped to price changes only.
UPDATE venues SET last_polled_at = now() WHERE slug = $1;
