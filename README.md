# Corridor

> **Google Flights for prediction markets.**
> Compare event odds across every venue. Catch the spread before anyone else does.

Corridor is a AI-native price-comparison and arbitrage-alert layer across prediction
market venues — Polymarket, Kalshi, Bayse, and more. The same real-world
event trades at different prices on every platform. Corridor finds the gap.

---

## What it does

| Feature | Status |
|---|---|
| Live odds ingestion (Polymarket + Kalshi) | ✅ Live |
| Cross-venue event matching | 🔨 Building (Phase 2) |
| Cross-venue arbitrage alerts (Telegram) | 📅 Planned (Phase 3) |
| African venue coverage (Bayse, OpinionMarket) | 📅 Planned |
| Historical odds API | 📅 Planned |
| Trade routing | 📅 Planned |

---

## Architecture

Modular monolith. One repo, one binary, four modules.
ingest/   → Go   — venue adapters, supervised polling, raw storage
match/    → Python — LLM-powered event matching across venues
spread/   → Go   — fee + FX adjusted spread engine, arb detection
notify/   → Go   — Telegram alerts, free channel + Pro tier

**Stack:** Go 1.22 · Python 3.12 · PostgreSQL 16 + TimescaleDB +
pgvector · Redis · Docker

**Prime directive:** ingestion never goes down.
The odds-history database is the moat. It cannot be backfilled.

---

## Getting started (local dev)

**Prerequisites:** Go 1.22+, Docker, `make`

```bash
git clone https://github.com/miracledoescode/corridor
cd corridor
cp .env.example .env          # fill in venue credentials
make up                       # postgres + timescale + redis
make migrate                  # run goose migrations
make run                      # start corridord
make verify                   # print venue/market/quote counts
```

---

## Project structure
```text
corridor/
├── CLAUDE.md                  # Claude Code's standing orders
├── README.md
├── Makefile                   # make up / verify / test / migrate / backup
├── docker-compose.yml         # timescaledb-ha, redis, corridord
├── .env.example
├── go.mod
├── cmd/
│   └── corridord/main.go      # ONE binary: ingest + spread + notify + api
├── internal/
│   ├── ingest/
│   │   ├── adapter.go         # interface: FetchMarkets / FetchQuotes / Health
│   │   ├── supervisor.go      # goroutine-per-venue, backoff restarts
│   │   ├── polymarket/
│   │   ├── kalshi/
│   │   └── bayse/
│   ├── store/                 # pgx + sqlc-generated queries
│   ├── spread/                # cross-venue math, fees, FX, arb detection
│   ├── notify/                # telegram dispatch (EXACT-only arbs)
│   └── api/                   # /healthz, /v1/events, /v1/quotes
├── match/                     # Python uv project — batch jobs, not a service
│   ├── pyproject.toml
│   ├── matcher/
│   │   ├── embed.py           # sentence-transformers → pgvector
│   │   ├── pair_llm.py        # LLM confirm/reject candidate pairs
│   │   ├── resolution_diff.py # settlement-rule diffing → confidence tier
│   │   └── review_queue.py    # human review CLI (10 min/day)
│   └── jobs/run_match.py      # cron entrypoint
├── migrations/                # goose SQL files
├── web/                       # static → Cloudflare Pages (Phase 4)
└── ops/
    ├── deploy.sh
    ├── backup.sh              # nightly pg_dump → R2
    └── caddy/Caddyfile
```
```text
corridor/
├── CLAUDE.md                 # AI coding assistant standing orders
├── Makefile
├── docker-compose.yml
├── cmd/corridord/main.go     # single binary entrypoint
├── internal/
│   ├── ingest/               # venue adapters + supervisor
│   ├── store/                # pgx + sqlc queries
│   ├── spread/               # arb math + FX adjustment
│   ├── notify/               # telegram dispatch
│   └── api/                  # /healthz + /v1/* REST
├── match/                    # Python matching engine (batch jobs)
├── migrations/               # goose SQL
├── web/                      # static frontend → Cloudflare Pages
└── ops/                      # deploy, backup, Caddyfile
```

---

## Venues

| Venue | Region | API |
|---|---|---|
| Polymarket | Global | Public (Gamma + CLOB) |
| Kalshi | US | Public REST + WS |
| Bayse | Nigeria / Africa | Reverse-engineered |
| More | — | Planned |

---

## Development principles

- Raw venue payloads always stored in JSONB — normalization is re-runnable, history is not
- `NUMERIC` for all prices — never `float`
- Arb alerts fire only on `EXACT` confidence matches (resolution-criteria verified)
- One goroutine per venue — one venue failing never affects another
- Idempotent upserts everywhere

---

## Status

**Phase 1 — Ingestion spine: ✅ complete and live on prod.** Polymarket +
Kalshi adapters running, supervised goroutine-per-venue with backoff restarts,
`/healthz`, idempotent upserts into **Supabase managed Postgres** (pgvector;
plain-Postgres schema, no TimescaleDB), raw payloads preserved in JSONB.
Verified in production against the live database: both venues ingesting, lag
< 20s, venue isolation proven under a real outage. RLS enabled on all tables.
See [`RUNBOOK.md`](./RUNBOOK.md) for venue reachability, the Supabase pooler
gotcha, and on-call notes.

| Phase | Scope | Status |
|---|---|---|
| 1 — Ingestion spine | Polymarket + Kalshi → Supabase Postgres, `/healthz` | ✅ live |
| 1b — Bayse adapter | Nigeria/Africa venue coverage | 📅 later |
| 2 — Matching engine | `match/` Python: embed → LLM pair → resolution diff | 🔨 next |
| 3 — Spread + notify | Fee/FX-adjusted arb detection, Telegram alerts | 📅 planned |
| 4 — Web | Static odds-comparison page on Cloudflare Pages | 📅 planned |

Next (Phase 2): the matching engine in `match/` — embed market titles into
pgvector, LLM-confirm cross-venue candidate pairs, diff resolution criteria
into confidence tiers, and fill `markets.event_id` + `market_matches`. Still
outstanding from Phase 1 ops: an always-on host for `corridord`, and nightly
`backup.sh` with a *proven* restore.

See [Notion workspace](https://app.notion.com/p/Corridor-Google-Flights-for-Prediction-Markets-37bc4b1c8bb081b5ab4af003519021eb?source=copy_link) for full product spec, roadmap, and decisions log.

---

## License

Proprietary. All rights reserved.
© 2026 Miracle Mathew, Corridor Labs
