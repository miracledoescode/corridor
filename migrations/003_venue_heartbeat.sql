-- +goose Up
-- WHY this column: as of Phase 1.5 the quotes table stores price CHANGES,
-- not fixed-interval samples (write-time dedup). That means MAX(quotes.time)
-- no longer answers "is ingestion alive?" — a market whose price hasn't
-- moved legitimately writes zero quotes for minutes, and the old
-- lag = now() - MAX(quotes.time) health signal would flip a perfectly
-- healthy venue to "degraded". last_polled_at is a per-venue heartbeat the
-- quote loop stamps every cycle regardless of whether any price moved, so
-- /healthz and `make verify` can distinguish "prices are stable" (fine)
-- from "ingestion is down" (the prime-directive nightmare). Nullable: a
-- venue that has never polled reads NULL = unknown, which health treats as
-- not-yet-healthy until the first cycle stamps it (~one quote interval).
ALTER TABLE venues ADD COLUMN IF NOT EXISTS last_polled_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE venues DROP COLUMN IF EXISTS last_polled_at;
