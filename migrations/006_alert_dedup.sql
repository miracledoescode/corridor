-- 006_alert_dedup.sql — make an alert an EPISODE, not a sighting.
--
-- WHY: the spread engine re-scans on a timer, but an arbitrage persists for
-- as long as the two books stay mispriced. Without a dedup key, a single
-- opportunity lasting five minutes would insert an alert row on every scan
-- and push an identical Telegram message each time. Ten notifications for one
-- trade is how an alert channel trains its subscribers to mute it — and the
-- Pro tier's entire value is that a message from it is worth reading.
--
-- The model: one row per EPISODE of an arb. dedup_key identifies the matched
-- pair AND the direction (YES on venue A + NO on venue B is a genuinely
-- different trade from its mirror, so the two alert independently). The row
-- stays open while the arb is live, and closed_at is stamped once it is gone.
-- If the same opportunity reappears later it is a new episode, and alerts
-- again — which is correct: it is news a second time.
--
-- WHY a partial UNIQUE index rather than app-side checking: it makes "at most
-- one OPEN alert per opportunity" an invariant the database enforces, so the
-- insert can be a plain ON CONFLICT DO NOTHING. That satisfies the idempotent-
-- upsert hard rule structurally — two engine ticks racing, or a restart
-- mid-scan, cannot produce a duplicate. Closed rows are deliberately excluded
-- from the constraint so history accumulates freely.
--
-- WHY a plain CREATE INDEX is safe here, unlike the markets embedding index:
-- this locks `alerts`, which no ingestion path writes. The venue metadata and
-- quote loops touch markets/outcomes/quotes only, so a brief lock on alerts
-- cannot stall ingestion. The prime directive is not in play.

-- +goose Up
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS dedup_key TEXT;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS alerts_open_dedup_idx
    ON alerts (dedup_key)
    WHERE closed_at IS NULL AND dedup_key IS NOT NULL;

-- Reading an alert's history for one opportunity, newest first.
CREATE INDEX IF NOT EXISTS alerts_dedup_created_idx
    ON alerts (dedup_key, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS alerts_dedup_created_idx;
DROP INDEX IF EXISTS alerts_open_dedup_idx;
ALTER TABLE alerts DROP COLUMN IF EXISTS closed_at;
ALTER TABLE alerts DROP COLUMN IF EXISTS dedup_key;
