# Contributing to Corridor

The contribution most wanted here is **a new venue adapter**. Corridor is only
as useful as the number of venues it can compare, and adding one is a
self-contained job behind a small interface.

Before anything else, please read the one rule everything else follows from.

## The prime directive

**Ingestion never goes down.** The odds-history database is the moat and it
cannot be backfilled — a market's price at 14:32 today is unavailable forever
if nothing recorded it. In any tradeoff, choose what keeps quotes flowing and
raw data stored.

Practically, for a contributor: your adapter runs inside a supervised loop
alongside every other venue. It must never be able to hang, crash, or slow
down another venue's capture. The architecture enforces most of this for you
(see [Venue isolation](#venue-isolation)), but it is worth knowing *why* the
review will ask about failure modes more than features.

## Getting set up

**You need to bring your own database.** `docker-compose.yml` has no Postgres
service, deliberately: a committed local-database override used to point
corridord at a throwaway container, which meant a fresh clone could silently
write the odds-history moat to ephemeral storage. The database is now always
whatever `DB_URL` says, and nothing overrides it.

It must have **pgvector** — `migrations/001` runs `CREATE EXTENSION vector`
and `markets.embedding` is `vector(384)`. A plain `postgres` image will fail
to migrate.

For local development, the quickest throwaway:

```bash
docker run -d --name corridor-pg \
  -e POSTGRES_USER=corridor -e POSTGRES_PASSWORD=corridor \
  -e POSTGRES_DB=corridor -p 5432:5432 \
  pgvector/pgvector:pg16
```

Then:

```bash
cp .env.example .env
# set DB_URL=postgres://corridor:corridor@localhost:5432/corridor?sslmode=disable
make up                  # redis sidecar + corridord container
make migrate             # apply schema
make run                 # start corridord on the host instead
make verify              # venue / market / quote counts
```

Good news on credentials: **ingestion needs none.** Both venue adapters read
public, keyless endpoints. Only the matcher needs a key (`GROQ_API_KEY`, free
tier is enough), and only the Telegram notifier needs a bot token — both are
optional and off by default.

The Python matcher is a separate, batch-only component:

```bash
cd match
uv sync
uv run python -m jobs.run_match
```

## Adding a venue adapter

Implement `ingest.Adapter` (`internal/ingest/adapter.go`) — four methods:

```go
type Adapter interface {
    FetchMarkets(ctx context.Context) ([]Market, error)
    FetchQuotes(ctx context.Context, venueMarketIDs []string) ([]Quote, error)
    Health(ctx context.Context) error
    Slug() string
}
```

Put it in `internal/ingest/<venue>/`, then register it in the `venues` slice in
`cmd/corridord/main.go` and add a row to the venue seed in
`migrations/001_init.sql`. `internal/ingest/polymarket/` and
`internal/ingest/kalshi/` are the two worked examples — read both, because they
solve the problem differently (Polymarket needs two APIs stitched together;
Kalshi paginates per series).

### Rules your adapter must follow

These are not style preferences. Each one exists because breaking it caused a
real problem.

**Prices are strings, never floats.** `float64` mangles decimals — `0.57`
becomes `0.56999...`. Carry the venue's exact decimal text end to end; it
converts to Postgres `NUMERIC` at the store boundary and nowhere else. An empty
string means NULL (the venue had no value), which is never the same as zero.

**Attach the raw payload.** Every `Market` and `Quote` carries its untouched
venue JSON in `Raw`. When a venue changes a field meaning under us, the raw
column is the only way to reconstruct what actually happened.

**Be idempotent.** Re-runs must never duplicate. The store upserts on
`(venue_id, venue_market_id)` and `(outcome_id, time)`; your job is to emit
stable ids, not to dedupe.

**Scope your sweep.** An unscoped Kalshi sweep once pulled 187,000
combinatorial parlay markets and took ingestion down for about 47 hours. The
fix is the `scopedSeries` allowlist in `internal/ingest/kalshi/adapter.go`, and
an *empty* allowlist refuses to fetch at all rather than defaulting to
everything. If your venue can enumerate a very large or combinatorial market
space, copy that pattern — fail closed, never open.

**Identify yourself and respect rate limits.** Every request carries the
shared User-Agent from `ingest.UserAgent`, and goes through
`ingest.NewClient`, which enforces the configured cap. Do not construct your
own `http.Client`.

**Never commit secrets.** Venue credentials belong in `.env`, which is
gitignored. `.env.example` documents the *shape* of a value, never a real one.

### Venue isolation

Each venue runs two independent supervised goroutines — one for metadata, one
for quotes — each restarted with backoff. They are split because a slow
metadata write must never freeze price capture. This means a failure in your
adapter degrades your venue and nothing else, *provided* you return errors
rather than panicking, and respect `ctx` cancellation.

## Tests

Go tests are table-driven (`internal/ingest/backoff_test.go` is the house
style). Adapters are tested against recorded fixtures with `httptest`, not the
live venue — see `internal/ingest/kalshi/adapter_test.go`.

```bash
go test ./...        # no infrastructure needed
gofmt -l .           # must print nothing
go vet ./...
cd match && uv run pytest
```

The store integration test skips itself unless `TEST_DB_URL` is set, so the
default run stays green without a database. CI runs it against a real
`pgvector` container.

## Pull requests

Small, conventional commits: `feat:`, `fix:`, `chore:`, `docs:`, `perf:`,
`refactor:`.

For anything non-obvious, include a short **WHY** paragraph — in the PR
description or as a code comment. The codebase is deliberately heavy on
comments explaining *why* a thing is the way it is, because the reasons are
usually a bug someone already paid for. Comments that restate what the code
does are not wanted; comments that record a constraint are.

If you are changing the schema, propose the migration diff before writing it.
Migrations run automatically at `corridord` boot and a failure stops the
process — which is to say, a bad migration is an ingestion outage.

CI must be green: `go test -race`, `go vet`, `gofmt`, `pytest`, and the
Postgres integration job.

## Scope

Corridor is a **data layer**. It compares prices and records history. It holds
no custody, places no bets, and executes no trades, and contributions that
would change that are out of scope.

Note also that venue terms of service vary on what may be done with their
data, particularly commercially. Corridor is self-hosted precisely so that
each operator runs under their own accounts and their own reading of those
terms. Please do not contribute code that redistributes a venue's data in ways
its terms prohibit.

## Licence

MIT. By contributing you agree your contribution is licensed under it too.
