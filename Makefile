# Local development tasks. These are thin wrappers around the real commands —
# read the recipe to see exactly what each one runs.
#

# Dev-only config, matching compose.yaml's Postgres. Never used in production.
# of these inline, e.g. `PORT=9090 make run`.
export DATABASE_URL   ?= postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable
export ADMIN_PASSWORD ?= dev-password
export ENCRYPTION_KEY ?= $(shell printf 'dev-32-byte-encryption-key-00000' | base64)
export LOG_FORMAT ?= text

.PHONY: db-up db-down db-reset run build tidy test test-integration e2e

# Start dependencies and block until Postgres is accepting connections.
db-up:
	docker compose up -d --wait

# Stop dependencies, keeping the data volume.
db-down:
	docker compose down

# Stop dependencies AND wipe the data volume — a clean slate for migrations.
db-reset:
	docker compose down -v

# Boot the gateway on the host against the dockerized Postgres.
run:
	go run ./cmd/gateway

build:
	go build ./...

tidy:
	go mod tidy

# Unit tests only, with the race detector. Integration tests skip themselves
# when TEST_DATABASE_URL is unset, so this stays green without a database.
test:
	go test -race ./...

# Full test suite including the Postgres-backed integration tests. Needs the
# compose Postgres up (`make db-up`); TEST_DATABASE_URL points the tests at it.
test-integration: db-up
	TEST_DATABASE_URL=$(DATABASE_URL) go test -race ./...

# Phase 1 end-to-end done-test: boots the gateway against compose Postgres and
# drives a signed + tampered webhook through the real HTTP pipeline.
e2e: db-up
	trap '$(MAKE) db-down' EXIT; ./test/e2e.sh
