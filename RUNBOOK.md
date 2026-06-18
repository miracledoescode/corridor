# Corridor Runbook

## Kalshi unreachable / zero quotes

Symptom: `/healthz` shows kalshi `healthy:false` with `last_quote_at:null`
while Polymarket ingests normally — every Kalshi request failing, not a
parse problem.

Checklist, in order:

1. **Host.** `KALSHI_BASE_URL` must point at the keyless public host
   `https://external-api.kalshi.com/trade-api/v2`.
   `api.elections.kalshi.com` is the authenticated trading host (key-signed
   requests); anonymous market-data polls fail there outright. This was the
   root cause of the 2026-06 day-one outage.
2. **Probe from the SAME network corridord runs on** (datacenter IPs can be
   treated differently from residential ones):
   `curl -sS -H "User-Agent: CorridorBot/0.1 (contact: miraclesayscode@gmail.com)" \
      "https://external-api.kalshi.com/trade-api/v2/markets?limit=2"`
   - `200` + JSON → host fine; look at corridord logs for the real error.
   - `403` + Cloudflare/challenge HTML → bot/IP filtering of this network.
     Do NOT add header spoofing, UA rotation, or proxy services — Corridor
     is a polite identified bot and that reputation is an asset. Move the
     workload (US-region / non-datacenter egress) instead.
   - `401` → endpoint now requires auth; re-check Kalshi docs for the
     current public host before doing anything else.
3. **Before choosing a prod region/host**: run the step-2 probe from the
   candidate network for BOTH venues first. Ingestion uptime is the prime
   directive — venue reachability is a deployment criterion, not an
   afterthought.

## Database: Supabase managed Postgres

Prod DB is Supabase (project `corridor`, eu-central-1). pgvector is enabled;
TimescaleDB is **not available** — the schema is plain Postgres on purpose so
the same goose migrations run on Supabase and local docker. RLS is enabled on
all public tables (migration `002`); corridord connects as the owner role and
bypasses RLS, so ingestion is unaffected.

### Topology
docker-compose runs only `redis` (a throwaway top-of-book sidecar) and
`corridord`. There is NO local Postgres service — Supabase is the database.
corridord reads `DB_URL` from `.env`; `make verify` / `make backup` /
`make migrate` all target that same `DB_URL`, so tooling can't drift to a
different DB than the one corridord writes. Local/offline dev = bring your
own Postgres and point `DB_URL` at it.

### Quotes are price CHANGES, not fixed-interval samples (Phase 1.5)
The quote write path dedups: a row is stored only when an outcome's
**bid/ask/last** differ from its last stored quote (volume/liquidity do NOT
trigger a write — they drift every cycle and would defeat dedup; the latest
values ride along on whatever row a price change produces). This cut row
growth ~5-20× to keep `quotes` under the Supabase free-tier cap; writing
every ~10s sample would otherwise cross 500MB in days and flip the project
read-only (ingestion writes fail — prime-directive nightmare).

Consequences anyone touching this must know:
- **Point-in-time price = the most recent quote row at or before T**, carried
  forward. There is NO row stamped exactly at each interval. The matcher,
  spread engine and charts must reconstruct "price at T" as `latest quote
  <= T`, never "row where time = T".
- **Liveness is `venues.last_polled_at`, NOT `MAX(quotes.time)`.** The quote
  loop stamps the heartbeat every cycle even when nothing changed, so a quiet
  market stays healthy. `/healthz` `lag_seconds` and `make verify`
  `lag_seconds` both measure now() − last_polled_at; `last_price_change` /
  `last_quote_at` are shown separately and may legitimately be minutes old.
  If you ever see lag climb on `MAX(quotes.time)` and panic — don't; check
  `last_polled_at` instead.
- An in-memory cache holds each outcome's last price; it's warmed from the DB
  on startup (`WarmDedupCache`) so a restart doesn't re-write every outcome.
  Worst case if warm-up fails: one redundant cycle of writes, then dedup
  resumes. Never fatal.

### Metadata and quotes run on SEPARATE loops per venue
Each venue has two supervised goroutines: a metadata loop (refresh markets,
`MetaEvery` ~60s) and a quote loop (top-of-book sweep, `QuoteEvery` ~10s).
WHY split: the metadata write upserts hundreds of markets to a REMOTE
Supabase. When it was per-market (a transaction each) it cost ~4 round trips ×
hundreds of markets ≈ tens of seconds, and because it shared the quote loop it
FROZE price capture for that whole stretch (polymarket was losing ~40-50s of
history per minute). Two fixes, both live:
- `UpsertMarkets` writes all markets then all outcomes as two pipelined
  batches in one transaction (~2 round trips, not ~1,600). Outcomes resolve
  `market_id` inline so they need nothing handed back from the market batch.
- Metadata polling moved to its own goroutine, so even a slow metadata write
  can't block quotes or the heartbeat.
If quote lag (`last_polled_at`) ever starts sawtoothing in lockstep with the
60s metadata tick again, suspect a regression that re-coupled the two loops or
un-batched the upsert.

### Connection string
- Use the **transaction POOLER** endpoint: host `...pooler.supabase.com`,
  **port 6543** — NOT the direct `:5432`. The direct endpoint's low
  connection cap would be exhausted by the supervisor's per-venue pool and
  stall ingestion.
- `sslmode=require` (Supabase needs TLS).
- URL-encode special characters in the password (`/`→`%2F`, `%`→`%25`,
  `@`→`%40`, …) or, simpler, set a password that's alphanumeric only.
  A raw `/` in the password is what caused the `invalid port` goose error.

### pgx + the transaction pooler — READ BEFORE TOUCHING CONNECTION CODE
The transaction pooler does **not** pin a logical connection to one backend
across round trips. Any pgx mode that relies on server-side prepared
statements (the default `CacheStatement`, and `DescribeExec`) will prepare on
one backend and execute on another → **`unnamed prepared statement does not
exist` (SQLSTATE 26000)**, which repeatedly crashed the Kalshi sweep on
cutover.

Fix, already in `internal/store/store.go` (both the pgxpool and the goose
connection):
- `DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol` — no prepared
  statements at all, so nothing for the pooler to lose between trips.
- Because simple protocol can't ask the server for parameter types, an
  `AfterConnect` hook registers Go `[]byte` as `jsonb` (`RegisterDefaultPgType`).
  Without it the `raw` JSONB columns get mis-encoded as bytea →
  `invalid input syntax for type json (22P02)`. Safe because we have no
  bytea columns.

If a future change reintroduces `26000`, the cause is almost always a code
path that bypassed this config (a new pool, a raw `pgxpool.New`, or a tool
like goose opened without simple protocol). Do not "fix" it by switching to
the session pooler or direct connection — keep simple protocol.
