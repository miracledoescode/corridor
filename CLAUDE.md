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

Phase 2: the matching engine in match/ (Python, uv). Goal: fill
markets.event_id and market_matches.

Pipeline:
1. embed.py — sentence-transformers (bge-small-en-v1.5, 384-dim) over
   market titles+descriptions → store in markets.embedding (pgvector).
2. pair_llm.py — for cross-venue candidates above cosine 0.80: ask the LLM
   (env-configured: DeepSeek or Gemini Flash) "same real-world event?"
   with pydantic-validated structured output {same: bool, why: str}.
3. resolution_diff.py — for confirmed pairs, diff resolution_criteria;
   output confidence EXACT | CHECK_TERMS | RELATED + a one-line diff note.
4. Write market_matches (market_a < market_b); link/create events rows.
5. review_queue.py — CLI showing un-reviewed matches; my keypress
   approves/rejects → sets reviewed_by_human.
Costs: batch, cache by content-hash so nothing is re-asked. Target <$3/mo.
Acceptance: World Cup markets across all venues matched; zero false EXACTs
in my manual spot-check of 30; jobs/run_match.py is cron-safe (idempotent).

Phase 3: internal/spread + internal/notify.
- Spread: for every EXACT match, compute cross-venue spread net of
  venues.fee_model and fx_rates (₦ via latest USDNGN_PARALLEL). Arb =
  YES@A + NO@B < 1.00 net. Write alerts rows. Include executable size
  estimate from liquidity field — never advertise an arb bigger than its book.
- FX job: poll exchange P2P USDT/NGN mid → fx_rates (every 10 min).
- Notify: telebot v4. Free channel: 2-3 delayed/watermarked spreads daily.
  Pro (whitelist of chat IDs for now): instant alerts. /event <q> searches
  events and prints a side-by-side odds table. Alert copy: terse, numeric,
  screenshot-friendly. Dispatch BEFORE any human (me) sees it — log
  dispatched_at first. (Front-running policy is product law.)
Acceptance: a real spread alert lands in my Telegram with venue, prices,
net edge, size, and links.

Phase 4: web/ — static page on Cloudflare Pages hitting GET /v1/events and
/v1/quotes (add CORS + 30s cache headers to internal/api).
htmx + Tailwind CDN, no build step. One screen: search box, table of events
sorted by spread, the "corridor" gap rendered as a shaded bar between venue
prices, 7-day sparkline per event. No login. Every row screenshot-ready:
clean numbers, venue logos, timestamp, corridor.app watermark.

Bug report. Observed: <what happened, exact output/logs>. Expected: <what
should happen>. Repro: <steps/command>. Recent changes: <last commits>.
Investigate root cause BEFORE proposing a fix; show me the evidence chain,
then the minimal fix, then the regression test that would have caught it.

Review this diff as a senior principal and a cybersecurity engineer: correctness, idempotency, venue
isolation, error handling, SQL safety, secrets hygiene. For each issue:
severity (blocker/should-fix/nit), why it matters, the fix. Then confirm:
does anything here risk the prime directive (ingestion uptime)?

Explain <concept/file/decision> like I'm a backend dev seeing it for the
first time: what it does, why it's here instead of the alternatives, how it
fails, and one exercise that would prove I understand it.
