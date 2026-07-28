"""resolution_diff.py — Step 3 of the matching pipeline.

For each LLM-confirmed pair, diff the resolution criteria and assign a
confidence tier:

  EXACT       — same event, same resolution rule (safe for arb alerts)
  CHECK_TERMS — same event, resolution differs in material ways
  RELATED     — same topic, but not the same question

Safety property: when uncertain, always demote. Never auto-promote to EXACT.
A false EXACT is the single biggest trust risk in this product.
"""
import os
from pydantic import BaseModel
from google import genai
from google.genai import types
from .db import get_conn

GEMINI_MODEL = "gemini-2.0-flash"


class DiffResult(BaseModel):
    confidence: str   # EXACT | CHECK_TERMS | RELATED
    diff_note: str    # one-line human-readable note


SYSTEM = (
    "You are a prediction-market analyst assessing whether two markets can be "
    "used for cross-venue arbitrage. Compare their resolution criteria and assign "
    "a confidence tier:\n"
    "  EXACT       — identical resolution rule; safe to treat as the same bet\n"
    "  CHECK_TERMS — same event but resolution differs in a material way (date, "
    "source, threshold, etc.) — a human must verify before trading\n"
    "  RELATED     — same topic but different questions\n"
    "When uncertain, always choose the lower tier. Never assign EXACT unless you "
    "are certain the resolution rules are equivalent. "
    "Return only the JSON schema provided."
)


def _diff(client: genai.Client, a: dict, b: dict) -> DiffResult:
    prompt = (
        f"Market A ({a['venue']}): {a['title']}\n"
        f"Resolution A: {a['resolution'] or 'not specified'}\n\n"
        f"Market B ({b['venue']}): {b['title']}\n"
        f"Resolution B: {b['resolution'] or 'not specified'}\n\n"
        "Assign confidence tier and write a one-line diff note."
    )
    resp = client.models.generate_content(
        model=GEMINI_MODEL,
        contents=prompt,
        config=types.GenerateContentConfig(
            system_instruction=SYSTEM,
            response_mime_type="application/json",
            response_schema=DiffResult,
        ),
    )
    return DiffResult.model_validate_json(resp.text)


def run(confirmed_pairs: list[tuple[int, int, str, str]]) -> None:
    """Write market_matches rows for each confirmed pair."""
    if not confirmed_pairs:
        return

    client = genai.Client(api_key=os.environ["GEMINI_API_KEY"])

    with get_conn() as conn:
        markets = {
            r[0]: r
            for r in conn.execute(
                """
                SELECT m.id, m.title, m.resolution_criteria, v.slug
                FROM markets m JOIN venues v ON v.id = m.venue_id
                WHERE m.id = ANY(%(ids)s)
                """,
                {"ids": list({id for pair in confirmed_pairs for id in pair[:2]})},
            ).fetchall()
        }

        written = 0
        for market_a, market_b, venue_a, venue_b in confirmed_pairs:
            a_row = markets[market_a]
            b_row = markets[market_b]

            result = _diff(
                client,
                {"venue": venue_a, "title": a_row[1], "resolution": a_row[2]},
                {"venue": venue_b, "title": b_row[1], "resolution": b_row[2]},
            )

            # Upsert — idempotent if run_match.py is re-run.
            conn.execute(
                """
                INSERT INTO market_matches
                    (market_a, market_b, confidence, resolution_diff, matched_by)
                VALUES (%s, %s, %s, %s, 'llm')
                ON CONFLICT (market_a, market_b) DO UPDATE SET
                    confidence      = EXCLUDED.confidence,
                    resolution_diff = EXCLUDED.resolution_diff,
                    matched_by      = EXCLUDED.matched_by
                """,
                (market_a, market_b, result.confidence, result.diff_note),
            )

            # Link both markets to a shared event row (create if needed).
            _link_event(conn, market_a, market_b, result.confidence)

            print(f"  [{result.confidence}] {a_row[1][:45]} / {b_row[1][:45]}")
            print(f"    {result.diff_note}")
            written += 1

        conn.commit()
        print(f"resolution_diff: wrote {written} match rows")


def _link_event(conn, market_a: int, market_b: int, confidence: str) -> None:
    """Ensure both markets share an events row.

    If either market already has an event_id, reuse it.
    Otherwise create a new event from market_a's title.
    Only links on EXACT or CHECK_TERMS — RELATED pairs don't share an event.
    """
    if confidence == "RELATED":
        return

    row = conn.execute(
        "SELECT event_id, title FROM markets WHERE id = ANY(%s)",
        ([market_a, market_b],),
    ).fetchall()

    existing_event = next((r[0] for r in row if r[0] is not None), None)

    if existing_event is None:
        title = next(r[1] for r in row if r[0] is None)
        result = conn.execute(
            "INSERT INTO events (canonical_title) VALUES (%s) RETURNING id",
            (title,),
        ).fetchone()
        existing_event = result[0]

    conn.execute(
        "UPDATE markets SET event_id = %s WHERE id = ANY(%s) AND event_id IS NULL",
        (existing_event, [market_a, market_b]),
    )
