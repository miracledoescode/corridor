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
