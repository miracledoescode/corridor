# Corridor

A cross-venue prediction market data layer — live ingestion, storage, and event-matching infrastructure for comparing odds across Polymarket, Kalshi, and beyond.

Think: the plumbing for "Google Flights for prediction markets."

---

## What it does

Corridor ingests live top-of-book quotes from multiple prediction market venues, deduplicates and stores them efficiently, and matches equivalent events across venues so you can compare odds on the same real-world question — and eventually detect arbitrage.

No custody. No execution. No gambling license required.

---

## Status

| Phase | Scope | Status |
|---|---|---|
| 1 — Ingestion spine | Polymarket + Kalshi → Postgres, `/healthz` | ✅ live |
| 2 — Matching engine | embed → LLM confirm → resolution diff → `market_matches` | 🔨 in progress |
| 3 — Spread + alerts | Fee/FX-adjusted arb detection, Telegram alerts | 📅 planned |
| 4 — Web | Static odds-comparison page on Cloudflare Pages | 📅 planned |

**Live numbers (as of July 2026):** 10,500+ markets ingested, 850K+ quotes captured, ~4s lag, Polymarket + Kalshi both running.

---

## What's built

- **Ingestion spine:** Live ingestion from Polymarket (~10,000 markets) and Kalshi (~130 active markets across crypto, US politics, sports, and macro). ~4s lag. Supervised goroutine-per-venue with backoff restarts — one venue failing never affects another.
- **Value-deduplication:** Quotes are written only on price change, not on every poll. 5–20x storage reduction vs. fixed-interval sampling.
- **Separate metadata and quote loops:** A slow metadata write can never freeze price capture. Each venue runs two independent supervised goroutines.
- **Row-level security:** RLS policies on all core tables.
- **Kalshi scoping guardrail:** An unscoped sweep once pulled 187K combinatorial parlay markets and crashed ingestion for ~47 hours. The fix is a `scopedSeries` allowlist in `internal/ingest/kalshi/adapter.go` — an empty allowlist refuses to sweep entirely. If you add venue coverage, respect this pattern.
- **Matching pipeline (in progress):** `match/` — sentence-transformers embeddings → cosine prefilter → Gemini Flash LLM confirmation → resolution-criteria diffing → `EXACT / CHECK_TERMS / RELATED` confidence tiers → human review CLI.

---

## The matcher's safety property

When uncertain, always demote. Never auto-promote to `EXACT`.

A false "these are the same market" match is the single biggest trust risk in this kind of product — it's what would cause a spread engine to advertise a fake arbitrage. The design enforces this at every step: the LLM must affirmatively confirm, the resolution diff must agree, and a human reviews before anything reaches `EXACT`. If you extend the matcher, keep this property.

---

## Getting started

**Prerequisites:** Go 1.22+, Python 3.12+, Docker, `make`, `uv`

```bash
git clone https://github.com/miracledoescode/corridor
cd corridor
cp .env.example .env          # fill in DB_URL (Supabase or local Postgres) + GEMINI_API_KEY
make up                       # redis sidecar
make migrate                  # run goose migrations
make run                      # start corridord (ingest + api)
make verify                   # print venue/market/quote counts
```

To run the matching pipeline:

```bash
cd match
uv sync
uv run python -m jobs.run_match      # embed → LLM confirm → write market_matches
uv run python -m matcher.review_queue  # human review CLI (y/n/s/q)
```

---

## Project structure

```text
corridor/
├── cmd/corridord/main.go      # single binary: ingest + api
├── internal/
│   ├── ingest/                # venue adapters + supervised polling
│   │   ├── adapter.go         # interface: FetchMarkets / FetchQuotes / Health
│   │   ├── supervisor.go      # goroutine-per-venue, backoff restarts
│   │   ├── polymarket/
│   │   └── kalshi/
│   ├── store/                 # pgx + sqlc-generated queries
│   ├── spread/                # cross-venue math, fees, FX (unfinished)
│   ├── notify/                # telegram dispatch (unfinished)
│   └── api/                   # /healthz, /v1/events, /v1/quotes
├── match/                     # Python matching engine (batch jobs)
│   ├── pyproject.toml         # uv project
│   ├── matcher/
│   │   ├── db.py              # shared psycopg3 + pgvector connection
│   │   ├── embed.py           # sentence-transformers → markets.embedding
│   │   ├── pair_llm.py        # cosine prefilter → Gemini confirms same event
│   │   ├── resolution_diff.py # resolution criteria diff → confidence tier
│   │   └── review_queue.py    # human review CLI
│   └── jobs/run_match.py      # cron entrypoint (idempotent)
├── migrations/                # goose SQL files
├── ops/                       # deploy.sh, backup.sh, Caddyfile
└── web/                       # static frontend placeholder
```

---

## Architecture

Modular monolith. One binary, one database, venue isolation via goroutines.

```
ingest/  → Go     — venue adapters, supervised polling, raw JSONB storage
match/   → Python — LLM-powered event matching (batch jobs)
spread/  → Go     — fee/FX-adjusted spread engine, arb detection (planned)
notify/  → Go     — Telegram alerts (planned)
```

**Stack:** Go 1.22 · Python 3.12 · PostgreSQL + pgvector · Redis · Docker

**Design principles:**
- Raw venue payloads stored in JSONB — normalization is re-runnable, history is not
- `NUMERIC` for all prices — never `float`
- One goroutine per venue — one venue failing never affects another
- Idempotent upserts everywhere
- Arb alerts fire only on `EXACT` confidence matches (resolution-criteria verified)

---

## Venues

| Venue | Region | API | Status |
|---|---|---|---|
| Polymarket | Global | Public (Gamma + CLOB) | ✅ live |
| Kalshi | US | Public REST | ✅ live |
| Bayse | Nigeria / Africa | — | 📅 planned |

**Kalshi series in scope:** crypto (BTC), US politics (SCOTUS, debt ceiling, stock trading ban), sports (NBA, NFL, F1, Club World Cup, EPL), macro (Fed), space (Starship). See `scopedSeries` in `internal/ingest/kalshi/adapter.go` to add more — but read the guardrail comment first.

---

## If you want to extend this

The ingestion spine plus an EXACT-match event graph accruing over time is the core asset. Both require always-on infrastructure — a laptop that sleeps doesn't count. Get it on a real host before investing in anything else.

The matcher's safety property (demote when unsure) is the one design decision worth protecting above all others. Everything else is negotiable.

See [`RUNBOOK.md`](./RUNBOOK.md) for the Supabase pooler gotcha, Kalshi host history, and on-call notes. Read it before touching connection code.

---

## License

MIT
