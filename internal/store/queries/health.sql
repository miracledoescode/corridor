-- name: VenueLag :many
SELECT v.slug, MAX(q.time)::timestamptz AS last_quote_at
FROM venues v
LEFT JOIN markets m  ON m.venue_id  = v.id
LEFT JOIN outcomes o ON o.market_id = m.id
LEFT JOIN quotes q   ON q.outcome_id = o.id
GROUP BY v.slug
ORDER BY v.slug;
