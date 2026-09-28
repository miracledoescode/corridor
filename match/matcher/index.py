"""index.py — ensures the HNSW index that pair_llm's k-NN search depends on.

WHY this lives here and not in migrations/: corridord runs goose at boot and
exits(1) if any migration fails (cmd/corridord/main.go). An index build that
failed would therefore take INGESTION down — and corridord never reads this
index at all. Only the Python matcher does. Keeping it matcher-owned means a
batch-job optimisation can never breach the prime directive.

Idempotent: IF NOT EXISTS makes every run after the first a cheap no-op, so
this is safe to call from the cron entrypoint on every pass.
"""
from psycopg import sql

from .db import get_conn

INDEX_NAME = "markets_embedding_idx"

# WHY hnsw over ivfflat: ivfflat needs representative data present at build
# time to cluster well and degrades as rows are added — bad for a table fed by
# continuous live ingestion. hnsw builds without training data and holds recall
# as markets accumulate.
#
# WHY vector_cosine_ops: embed.py writes normalized vectors and every query
# uses the <=> (cosine distance) operator. If the opclass does not match the
# operator the planner silently ignores the index.
_CREATE = sql.SQL(
    "CREATE INDEX CONCURRENTLY IF NOT EXISTS {name} "
    "ON markets USING hnsw (embedding vector_cosine_ops)"
)

_INVALID_CHECK = """
    SELECT 1
    FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    WHERE c.relname = %s AND NOT i.indisvalid
"""


def ensure() -> None:
    with get_conn(autocommit=True) as conn:
        if conn.execute(_INVALID_CHECK, (INDEX_NAME,)).fetchone():
            # WHY warn rather than rebuild: an interrupted CONCURRENTLY build
            # leaves an INVALID index, and CREATE INDEX ... IF NOT EXISTS then
            # sees the name and no-ops. The planner ignores an invalid index,
            # so queries stay correct but silently fall back to the sequential
            # scan this index exists to avoid. Dropping it is a deliberate act
            # against prod, so it is the operator's call, not this job's.
            print(
                f"index: {INDEX_NAME} exists but is INVALID (interrupted build). "
                "k-NN search is falling back to a sequential scan. "
                f"Fix with: DROP INDEX CONCURRENTLY {INDEX_NAME};"
            )
            return

        conn.execute(_CREATE.format(name=sql.Identifier(INDEX_NAME)))
        print(f"index: {INDEX_NAME} ready")


if __name__ == "__main__":
    ensure()
