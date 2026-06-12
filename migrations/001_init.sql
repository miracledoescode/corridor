-- 001_init.sql — full Corridor schema.
--
-- WHY all phases' tables now: the schema is the contract between the Go
-- ingestion spine and the Python matcher. Creating it once avoids a churny
-- migration series, and empty tables cost nothing at runtime.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS timescaledb;
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE venues (
    id            SMALLSERIAL PRIMARY KEY,
    slug          TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    base_currency TEXT NOT NULL DEFAULT 'USD',
    fee_model     JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id              BIGSERIAL PRIMARY KEY,
    canonical_title TEXT NOT NULL,
    category        TEXT,
    embedding       vector(384),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE markets (
    id                  BIGSERIAL PRIMARY KEY,
    venue_id            SMALLINT NOT NULL REFERENCES venues(id),
    event_id            BIGINT REFERENCES events(id),
    venue_market_id     TEXT NOT NULL,
    title               TEXT NOT NULL,
    resolution_criteria TEXT,
    close_time          TIMESTAMPTZ,
    status              TEXT NOT NULL DEFAULT 'active',
    embedding           vector(384),
    raw                 JSONB NOT NULL,
    first_seen          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue_id, venue_market_id)
);
CREATE INDEX markets_status_idx   ON markets (status);
CREATE INDEX markets_event_id_idx ON markets (event_id);

CREATE TABLE outcomes (
    id               BIGSERIAL PRIMARY KEY,
    market_id        BIGINT NOT NULL REFERENCES markets(id),
    venue_outcome_id TEXT NOT NULL,
    label            TEXT NOT NULL,
    UNIQUE (market_id, venue_outcome_id)
);

CREATE TABLE quotes (
    time       TIMESTAMPTZ NOT NULL,
    outcome_id BIGINT NOT NULL REFERENCES outcomes(id),
    bid        NUMERIC(8,5),
    ask        NUMERIC(8,5),
    last       NUMERIC(8,5),
    volume_24h NUMERIC,
    liquidity  NUMERIC,
    raw        JSONB
);
SELECT create_hypertable('quotes', 'time');
-- WHY unique: the idempotency hard rule — a retry inserting the same
-- (outcome, timestamp) snapshot must be a no-op, never a duplicate row.
-- The same index satisfies the required (outcome_id, time DESC) lookup.
-- TimescaleDB allows it because the partition column (time) is included.
CREATE UNIQUE INDEX quotes_outcome_time_idx ON quotes (outcome_id, time DESC);

CREATE TABLE market_matches (
    id                BIGSERIAL PRIMARY KEY,
    market_a          BIGINT NOT NULL REFERENCES markets(id),
    market_b          BIGINT NOT NULL REFERENCES markets(id),
    confidence        TEXT NOT NULL,
    resolution_diff   TEXT,
    matched_by        TEXT,
    reviewed_by_human BOOLEAN NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (market_a < market_b),
    UNIQUE (market_a, market_b)
);

CREATE TABLE fx_rates (
    time   TIMESTAMPTZ NOT NULL,
    pair   TEXT NOT NULL,
    rate   NUMERIC NOT NULL,
    source TEXT NOT NULL
);
SELECT create_hypertable('fx_rates', 'time');
CREATE UNIQUE INDEX fx_rates_pair_source_time_idx ON fx_rates (pair, source, time DESC);

CREATE TABLE alerts (
    id            BIGSERIAL PRIMARY KEY,
    kind          TEXT NOT NULL,
    event_id      BIGINT REFERENCES events(id),
    payload       JSONB NOT NULL,
    net_edge      NUMERIC,
    dispatched_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed the Phase 1 venues (idempotent re-runs).
INSERT INTO venues (slug, name, base_currency, fee_model) VALUES
    ('polymarket', 'Polymarket', 'USD', '{}'),
    ('kalshi',     'Kalshi',     'USD', '{}')
ON CONFLICT (slug) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS fx_rates;
DROP TABLE IF EXISTS market_matches;
DROP TABLE IF EXISTS quotes;
DROP TABLE IF EXISTS outcomes;
DROP TABLE IF EXISTS markets;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS venues;
