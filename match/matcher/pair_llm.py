"""pair_llm.py — Step 2 of the matching pipeline.

For every cross-venue market pair with cosine similarity >= THRESHOLD,
ask Gemini Flash "are these the same real-world event?" and collect
confirmed pairs for resolution_diff.py.

Idempotent: pairs already in market_matches are skipped.
Cost control: each (market_a, market_b) pair is asked at most once.
"""
import os
import time
from pydantic import BaseModel
from google import genai
from google.genai import types
from .db import get_conn

THRESHOLD = 0.85
GEMINI_MODEL = "gemini-2.0-flash-lite"
MAX_CANDIDATES = 500   # safety cap per run — raise once on paid tier
RPM_LIMIT = 10         # free tier: 15 RPM; stay under with headroom


class Verdict(BaseModel):
    same: bool
    why: str


SYSTEM = (
    "You are a prediction-market analyst. "
    "Given two market titles and their resolution criteria, decide if they resolve "
    "on the exact same real-world event. Answer only with the JSON schema provided."
)


def _ask(client: genai.Client, a: dict, b: dict) -> Verdict:
    prompt = (
        f"Market A ({a['venue']}):\n"
        f"Title: {a['title']}\n"
        f"Resolution: {a['resolution'] or 'not specified'}\n\n"
        f"Market B ({b['venue']}):\n"
        f"Title: {b['title']}\n"
        f"Resolution: {b['resolution'] or 'not specified'}\n\n"
        "Same real-world event?"
    )
    resp = client.models.generate_content(
        model=GEMINI_MODEL,
        contents=prompt,
        config=types.GenerateContentConfig(
            system_instruction=SYSTEM,
            response_mime_type="application/json",
            response_schema=Verdict,
        ),
    )
    return Verdict.model_validate_json(resp.text)


def run() -> list[tuple[int, int, str, str]]:
    """Return confirmed pairs as (market_a_id, market_b_id, venue_a, venue_b)."""
    client = genai.Client(api_key=os.environ["GEMINI_API_KEY"])

    with get_conn() as conn:
        # Cross-venue candidates above threshold, excluding already-matched pairs.
        # market_a < market_b enforced by the CHECK constraint in market_matches.
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
        time.sleep(60 / RPM_LIMIT)  # stay within free-tier RPM cap

    print(f"pair_llm: {len(confirmed)}/{len(candidates)} confirmed")
    return confirmed


if __name__ == "__main__":
    run()
