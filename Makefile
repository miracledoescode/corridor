SHELL := /bin/bash

# Load .env so host-side targets (run, migrate) see the same config the
# container gets via env_file.
-include .env
export

.PHONY: up down migrate verify test run backup sqlc

up:
	docker compose up -d --build

down:
	docker compose down

# WHY the @version suffix: it runs the goose CLI in module-less mode, so
# its extra DB drivers (mysql etc.) don't have to pollute our go.sum.
# corridord also migrates itself at boot; this target is for migrating
# without starting ingestion.
migrate:
	go run github.com/pressly/goose/v3/cmd/goose@v3.27.1 -dir migrations postgres "$(DB_URL)" up

verify:
	@psql "$(DB_URL)" -c "\
	SELECT v.slug, \
	       COUNT(DISTINCT m.id) AS markets, \
	       COUNT(q.time)        AS quotes, \
	       MAX(q.time)          AS newest_quote, \
	       EXTRACT(EPOCH FROM (now() - MAX(q.time)))::int AS lag_seconds \
	FROM venues v \
	LEFT JOIN markets m  ON m.venue_id  = v.id \
	LEFT JOIN outcomes o ON o.market_id = m.id \
	LEFT JOIN quotes q   ON q.outcome_id = o.id \
	GROUP BY v.slug;"

test:
	go test ./...

run:
	go run ./cmd/corridord

backup:
	@mkdir -p backups
	@pg_dump "$(DB_URL)" -Fc > backups/corridor_$$(date +%Y%m%dT%H%M%S).dump

# Regenerate internal/store/gen from the .sql query files.
# WHY pinned via go run: no global install needed; everyone gets the same
# sqlc version the module pins in go.mod (tools.go).
sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc generate
