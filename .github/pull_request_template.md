## What this changes

<!-- One or two sentences. -->

## WHY

<!--
The reason behind the change, not a restatement of the diff. What constraint,
failure, or surprise made this the right shape? This is the part that is
worth reading in six months.
-->

## Test plan

<!-- How you know it works. Tick what you ran; delete what does not apply. -->

- [ ] `go test ./...`
- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...`
- [ ] `cd match && uv run pytest`
- [ ] Ran against a real database / live venue

## Checks

- [ ] No secrets in the diff (`.env` stays gitignored; `.env.example` shows shape, never a real value)
- [ ] Prices stay decimal strings / `NUMERIC` — no `float64` anywhere near a price
- [ ] Re-running this does not duplicate rows
- [ ] Ingestion cannot be stalled by this change

<!--
If this adds a dependency or changes the schema, say so explicitly — both
want agreement before the code, and migrations run at corridord boot, so a
bad one is an ingestion outage.
-->
