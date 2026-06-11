# Corridor — CLAUDE.md

## What this is
Corridor is "Google Flights for prediction markets": a price-comparison and
arbitrage-alert layer across prediction market venues (Polymarket, Kalshi,
Bayse; more later). Pure data layer — no custody, no bets, no execution.
The founder is customer zero (a day trader).

## Prime directive
INGESTION NEVER GOES DOWN. The odds-history database is the moat and cannot
be backfilled. In any tradeoff, choose what keeps quotes flowing and raw
data stored.

## Architecture (do not deviate without asking)
- Modular monolith, one repo. Go binary `corridord` = ingest + spread +
  notify + api. Python `match/` = batch jobs, not a service.
- Postgres 16 + TimescaleDB + pgvector. Redis = live top-of-book only.
- Raw venue payloads ALWAYS stored in JSONB next to normalized rows.
- One adapter per venue: FetchMarkets / FetchQuotes / Health.
- Venue isolation: supervised goroutine per venue, backoff restarts — one
  venue failing must never affect another.

## Hard rules
- Idempotent upserts; re-runs never duplicate.
- NUMERIC for prices. Never float.
- Arb alerts fire ONLY on confidence = EXACT matches.
- Identified User-Agent on every venue request; respect configured rate caps.
- No secrets in code or commits — .env only. If you ever see a key, stop
  and tell me.
- Ask before adding any dependency.
- In any new area: propose the file tree / migration diff first, wait for
  approval, then code in small conventional commits (feat:/fix:/chore:).

## Style
- Go: stdlib-first, slog logging, table-driven tests, sqlc for queries, chi router.
- Python: uv, pydantic for LLM structured outputs, type hints everywhere.

## Founder context — important
I am learning while building. For every non-obvious decision, add a short
"WHY:" paragraph in the PR description or comment. Teach, don't dumb down.

## Current phase
Phase 1: ingestion spine (Polymarket + Kalshi). Do NOT build the matcher,
spread engine, Telegram bot, or web ahead of phase.
