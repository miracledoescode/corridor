"""embed.py — Step 1 of the matching pipeline.

Loads all markets with a null embedding, encodes title + resolution_criteria
with bge-small-en-v1.5 (384-dim), and writes back to markets.embedding.

Idempotent: only processes rows where embedding IS NULL, so re-runs are safe.
Content-hash skipping is not needed here — a null embedding IS the signal.
"""
import hashlib
from sentence_transformers import SentenceTransformer
from .db import get_conn

MODEL = "BAAI/bge-small-en-v1.5"
BATCH = 256


def _text(title: str, resolution: str | None) -> str:
    """Combine title and resolution criteria into one embedding input."""
    if resolution:
        return f"{title}\n{resolution}"
    return title


def run() -> None:
    model = SentenceTransformer(MODEL)

    with get_conn() as conn:
        rows = conn.execute(
            """
            SELECT id, title, resolution_criteria
            FROM markets
            WHERE embedding IS NULL
            ORDER BY id
            """
        ).fetchall()

        if not rows:
            print("embed: nothing to encode")
            return

        print(f"embed: encoding {len(rows)} markets with {MODEL}")

        ids = [r[0] for r in rows]
        texts = [_text(r[1], r[2]) for r in rows]

        embeddings = model.encode(
            texts,
            batch_size=BATCH,
            show_progress_bar=True,
            normalize_embeddings=True,  # cosine sim = dot product on unit vectors
        )

        with conn.cursor() as cur:
            cur.executemany(
                "UPDATE markets SET embedding = %s WHERE id = %s",
                [(vec, market_id) for market_id, vec in zip(ids, embeddings)],
            )
        conn.commit()
        print(f"embed: wrote {len(ids)} embeddings")


if __name__ == "__main__":
    run()
