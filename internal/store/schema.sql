-- sqlc type-checking schema ONLY — the real database is built from
-- migrations/. This mirrors 001_init.sql minus what sqlc cannot parse:
-- create_hypertable() calls and vector(384) columns (no query touches
-- embeddings; that is Phase 2 Python territory).

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
    raw                 JSONB NOT NULL,
    first_seen          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue_id, venue_market_id)
);

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
CREATE UNIQUE INDEX quotes_outcome_time_idx ON quotes (outcome_id, time DESC);
