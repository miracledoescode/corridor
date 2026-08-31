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


def run() -> list[tuple[int, int, str, str]]:
    """Return confirmed pairs as (market_a_id, market_b_id, venue_a, venue_b)."""
    client = Groq(api_key=os.environ["GROQ_API_KEY"])

    with get_conn() as conn:
        candidates = conn.execute(
            """
            SELECT
                LEAST(a.id, b.id)    AS market_a,
                GREATEST(a.id, b.id) AS market_b,
                a.title              AS title_a,
                a.resolution_criteria AS res_a,
                va.slug              AS venue_a,
                b.title              AS title_b,
                b.resolution_criteria AS res_b,
                vb.slug              AS venue_b,
                1 - (a.embedding <=> b.embedding) AS similarity
            FROM markets a
            JOIN venues va ON va.id = a.venue_id
            JOIN markets b ON b.id > a.id
            JOIN venues vb ON vb.id = b.venue_id
            WHERE va.id != vb.id
              AND a.status = 'active'
              AND b.status = 'active'
              AND a.embedding IS NOT NULL
              AND b.embedding IS NOT NULL
              AND 1 - (a.embedding <=> b.embedding) >= %(threshold)s
              AND NOT EXISTS (
                  SELECT 1 FROM market_matches mm
                  WHERE mm.market_a = LEAST(a.id, b.id)
                    AND mm.market_b = GREATEST(a.id, b.id)
              )
            ORDER BY similarity DESC
            LIMIT %(limit)s
            """,
            {"threshold": THRESHOLD, "limit": MAX_CANDIDATES},
        ).fetchall()

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
