"""pair_llm.py — Step 2 of the matching pipeline.

For every cross-venue market pair with cosine similarity >= THRESHOLD,
ask an LLM "are these the same real-world event?" and collect confirmed
pairs for resolution_diff.py.

Idempotent: pairs already in market_matches are skipped.
"""
import json
import os
import time
from pydantic import BaseModel
from groq import Groq
from .db import get_conn

THRESHOLD = 0.88
# WHY: Keep the model configurable because Groq retires model IDs and model
# access can vary by account. The default is a currently supported fallback.
GROQ_MODEL = os.getenv("GROQ_MODEL", "llama-3.3-70b-versatile")
MAX_CANDIDATES = 500
RPM_LIMIT = 20  # conservative to avoid rate limits
# WHY: only the nearest few cross-venue neighbours can plausibly be the same
# event. Anything past that is far below THRESHOLD anyway, so fetching more
# just costs index work.
TOP_K = 5


class Verdict(BaseModel):
    same: bool
    why: str


SYSTEM = (
    "You are a prediction-market analyst. "
    "Given two market titles and their resolution criteria, decide if they resolve "
    "on the exact same real-world event. "
    'Respond with JSON matching this schema: {"same": boolean, "why": string}'
)


def _ask(client: Groq, a: dict, b: dict) -> Verdict:
    def _res(r): return (r or "not specified")[:300]
    prompt = (
        f"Market A ({a['venue']}):\n"
        f"Title: {a['title']}\n"
        f"Resolution: {_res(a['resolution'])}\n\n"
        f"Market B ({b['venue']}):\n"
        f"Title: {b['title']}\n"
        f"Resolution: {_res(b['resolution'])}\n\n"
        "Same real-world event?"
    )
    resp = client.chat.completions.create(
        model=GROQ_MODEL,
        messages=[
            {"role": "system", "content": SYSTEM},
            {"role": "user", "content": prompt},
        ],
        response_format={"type": "json_object"},
        temperature=0,
    )
    return Verdict.model_validate(json.loads(resp.choices[0].message.content))


def venue_pairs(sizes: list[tuple[int, int]]) -> list[tuple[int, int]]:
    """Ordered (driver, other) venue pairs, smaller venue driving the scan.

    WHY smaller-first: the driver side is scanned row by row while the other
    side is served by the HNSW index. Driving from the 130-market venue means
    130 index probes; driving from the 10,000-market venue means 10,000 probes
    hunting a handful of needles. Same pairs either way, ~75x the work.
    """
    ordered = [venue_id for venue_id, _ in sorted(sizes, key=lambda s: s[1])]
    return [
        (driver, other)
        for i, driver in enumerate(ordered)
        for other in ordered[i + 1:]
    ]


# WHY LATERAL: this asks the index "what are the k nearest markets to THIS
# one?" once per driving market. The previous form asked for every pair under
# a distance threshold, which pgvector's ANN indexes cannot answer at all —
# see migrations/004_embedding_index.sql.
_CANDIDATE_SQL = """
    SELECT
        LEAST(a.id, nn.id)    AS market_a,
        GREATEST(a.id, nn.id) AS market_b,
        a.title               AS title_a,
        a.resolution_criteria AS res_a,
        va.slug               AS venue_a,
        nn.title              AS title_b,
        nn.resolution_criteria AS res_b,
        vb.slug               AS venue_b,
        nn.similarity
    FROM markets a
    JOIN venues va ON va.id = a.venue_id
    CROSS JOIN LATERAL (
        SELECT
            b.id, b.title, b.resolution_criteria, b.venue_id,
            1 - (b.embedding <=> a.embedding) AS similarity
        FROM markets b
        WHERE b.venue_id = %(other_venue)s
          AND b.status = 'active'
          AND b.embedding IS NOT NULL
        ORDER BY b.embedding <=> a.embedding
        LIMIT %(top_k)s
    ) nn
    JOIN venues vb ON vb.id = nn.venue_id
    WHERE a.venue_id = %(driver_venue)s
      AND a.status = 'active'
      AND a.embedding IS NOT NULL
      AND nn.similarity >= %(threshold)s
      AND NOT EXISTS (
          SELECT 1 FROM market_matches mm
          WHERE mm.market_a = LEAST(a.id, nn.id)
            AND mm.market_b = GREATEST(a.id, nn.id)
      )
    ORDER BY nn.similarity DESC
    LIMIT %(limit)s
"""


def run() -> list[tuple[int, int, str, str]]:
    """Return confirmed pairs as (market_a_id, market_b_id, venue_a, venue_b)."""
    client = Groq(api_key=os.environ["GROQ_API_KEY"])

    with get_conn() as conn:
        sizes = conn.execute(
            """
            SELECT venue_id, count(*)
            FROM markets
            WHERE status = 'active' AND embedding IS NOT NULL
            GROUP BY venue_id
            """
        ).fetchall()

        candidates = []
        for driver_venue, other_venue in venue_pairs(sizes):
            candidates.extend(
                conn.execute(
                    _CANDIDATE_SQL,
                    {
                        "driver_venue": driver_venue,
                        "other_venue": other_venue,
                        "top_k": TOP_K,
                        "threshold": THRESHOLD,
                        "limit": MAX_CANDIDATES,
                    },
                ).fetchall()
            )

        candidates.sort(key=lambda r: r[8], reverse=True)
        candidates = candidates[:MAX_CANDIDATES]

    if not candidates:
        print("pair_llm: no new candidates above threshold")
        return []

    print(f"pair_llm: {len(candidates)} candidates to evaluate")

    confirmed = []
    for row in candidates:
        market_a, market_b, title_a, res_a, venue_a, title_b, res_b, venue_b, sim = row
        verdict = _ask(
            client,
            {"venue": venue_a, "title": title_a, "resolution": res_a},
            {"venue": venue_b, "title": title_b, "resolution": res_b},
        )
        status = "✓" if verdict.same else "✗"
        print(f"  {status} [{sim:.2f}] {title_a[:50]} / {title_b[:50]}")
        if verdict.same:
            confirmed.append((market_a, market_b, venue_a, venue_b))
        time.sleep(60 / RPM_LIMIT)

    print(f"pair_llm: {len(confirmed)}/{len(candidates)} confirmed")
    return confirmed


if __name__ == "__main__":
    run()
