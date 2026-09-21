-- 004_embedding_index.sql — HNSW index on markets.embedding.
--
-- WHY this index: pair_llm.py's candidate search used to be a cross-venue
-- self-join with a similarity THRESHOLD predicate:
--
--     FROM markets a JOIN markets b ON b.id > a.id
--     WHERE 1 - (a.embedding <=> b.embedding) >= 0.88
--
-- At ~10,000 Polymarket x ~130 Kalshi active markets that is ~1.3M 384-dim
-- distance computations per run, and it only grows as venue coverage widens.
-- No index can rescue that shape, because pgvector's ANN indexes answer
-- "k nearest to THIS vector" (ORDER BY ... LIMIT k), NOT "all pairs within a
-- radius" — a threshold predicate inside a join always falls back to a scan.
-- So pair_llm was rewritten to a per-market k-NN (LATERAL ... ORDER BY
-- embedding <=> a.embedding LIMIT k), which is the shape this index serves.
--
-- WHY hnsw and not ivfflat: ivfflat needs representative data present at
-- build time to cluster well, and it degrades as rows are added — bad for a
-- table that grows continuously from live ingestion. hnsw builds without
-- training data and keeps recall stable as markets accumulate.
--
-- WHY vector_cosine_ops: embed.py writes normalized vectors and every query
-- uses the <=> (cosine distance) operator. The opclass must match the
-- operator or the planner silently ignores the index.

-- +goose NO TRANSACTION
-- +goose Up
-- WHY CONCURRENTLY: corridord runs goose at boot, and a plain CREATE INDEX
-- takes an ACCESS EXCLUSIVE lock on markets — which would block the venue
-- metadata loop mid-write and stall ingestion on every deploy. That is the
-- prime directive violated by a migration. CONCURRENTLY trades a slower
-- build for not blocking writers. It cannot run inside a transaction, hence
-- the NO TRANSACTION annotation above.
--
-- NOTE: if a CONCURRENTLY build is interrupted it leaves an INVALID index
-- behind, which the planner ignores (queries stay correct, just slow). The
-- IF NOT EXISTS below will NOT retry it — drop the invalid index manually,
-- then re-run. Check with:
--     SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid;
CREATE INDEX CONCURRENTLY IF NOT EXISTS markets_embedding_idx
    ON markets USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS markets_embedding_idx;
