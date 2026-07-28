# Corridor

A cross-venue prediction market data layer — ingestion, storage, and event-matching infrastructure for comparing odds across Polymarket, Kalshi, and beyond.

**Status: archived / open source.** This was a startup for a while. It isn't anymore. The founder decided to stop pursuing it as a company and is releasing the codebase so others can pick it up, fork it, or just study how it's built.

---

## What this actually is

Corridor is a pure data layer — no custody, no execution, no gambling license required. It ingests live quotes from multiple prediction market venues, stores them efficiently, and (in progress) matches equivalent events across venues so you can compare odds on the same real-world question.

Think: the plumbing for "Google Flights for prediction markets," minus the flights UI.

---

## What's built and working

- **Ingestion spine (complete):** Live ingestion from Polymarket (1,164 markets) and Kalshi (172 markets), ~4s lag, 716K+ quotes captured, running on Postgres/Supabase with pgvector.
- **Value-deduplication:** 5–20x storage reduction on quote history by only writing on price change.
- **Row-level security:** RLS policies across all core tables.
- **Supervised ingestion goroutines:** separate supervision for metadata vs. live quote streams, so one doesn't take down the other.
- **Kalshi scoping guardrail:** a hard-won lesson — an unscoped sweep once pulled 187K combinatorial markets and crashed ingestion for ~47 hours. The fix is a `scopedSeries` allowlist in `internal/ingest/kalshi/adapter.go`; an empty allowlist refuses to sweep at all. If you extend venue coverage, respect this pattern.

---

## What's designed but not finished

- **Event-matcher (Phase 2):** Architecture is spec'd — local embeddings prefilter → LLM pair confirmation → resolution-criteria diffing → confidence tiers (EXACT / CHECK-TERMS / RELATED) → permanent match cache with daily human review. Safety property baked into the design: when uncertain, always demote, never auto-promote to EXACT. This matters — a false "these are the same market" match is the single biggest trust risk in this kind of product. If you build this out, keep that property.
- Historical odds API, spread engine, Telegram alerts, and a 4th venue integration were all designed but never finished.

---

## Why it's archived

Short version: no edge, no users, no validation strong enough to justify continuing as a company. The founder ran limited customer discovery and didn't find enough signal that people wanted a cross-venue comparison tool badly enough — most retail prediction market traders appear to stick to one venue. That's a real, if informal, finding — take it as a data point, not gospel, if you're considering building a product on top of this.

---

## If you want to pick this up

The moat, such as it was, was always the ingestion spine plus an EXACT-match event graph accruing over time. Both require always-on infrastructure to be worth anything — a laptop that sleeps doesn't count. If you're serious about extending this, get it on a real always-on host before you invest in anything else.

The matcher's safety property (demote when unsure) is the one design decision worth protecting above all others. Everything else is negotiable.

---

## Getting started

**Prerequisites:** Go 1.22+, Docker, `make`

```bash
git clone https://github.com/miracledoescode/corridor
cd corridor
cp .env.example .env          # fill in your own DB_URL (Supabase or local Postgres)
make up                       # redis sidecar
make migrate                  # run goose migrations
make run                      # start corridord
make verify                   # print venue/market/quote counts
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
├── match/                     # Python matching engine (batch jobs, unfinished)
│   └── matcher/
│       ├── embed.py           # sentence-transformers → pgvector
│       ├── pair_llm.py        # LLM confirm/reject candidate pairs
│       ├── resolution_diff.py # settlement-rule diffing → confidence tier
│       └── review_queue.py    # human review CLI
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
spread/  → Go     — fee + FX adjusted spread engine, arb detection
notify/  → Go     — Telegram alerts
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

| Venue | Region | API |
|---|---|---|
| Polymarket | Global | Public (Gamma + CLOB) |
| Kalshi | US | Public REST |
| Bayse | Nigeria / Africa | Planned |

---

## License

MIT
