"""run_match.py — cron entrypoint for the full matching pipeline.

Run order:
  1. embed      — encode any markets with null embeddings
  2. pair_llm   — find cross-venue candidates, LLM-confirm
  3. resolution_diff — diff resolution criteria, write market_matches + events

Idempotent: safe to run on a schedule (cron, GitHub Actions, etc.).
Each step skips work that's already done.

Usage:
    uv run python -m jobs.run_match
"""
from matcher import embed, pair_llm, resolution_diff


def main() -> None:
    print("=== corridor matcher ===")
    embed.run()
    confirmed = pair_llm.run()
    resolution_diff.run(confirmed)
    print("=== done ===")


if __name__ == "__main__":
    main()
