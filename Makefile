# Local development tasks. These are thin wrappers around the real commands —
# read the recipe to see exactly what each one runs.
#

# Dev-only config, matching compose.yaml's Postgres. Never used in production.
# of these inline, e.g. `PORT=9090 make run`.
export DATABASE_URL   ?= postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable
export ADMIN_PASSWORD ?= dev-password
export ENCRYPTION_KEY ?= $(shell printf 'dev-32-byte-encryption-key-00000' | base64)
export LOG_FORMAT ?= text

.PHONY: db-up db-down db-reset run build build-go web web-dev tidy test \
        test-integration e2e cli-build cli-test e2e-tunnel

# Start dependencies and block until Postgres is accepting connections.
db-up:
	docker compose up -d --wait

# Stop dependencies, keeping the data volume.
db-down:
	docker compose down

# Stop dependencies AND wipe the data volume — a clean slate for migrations.
db-reset:
	docker compose down -v

# Boot the gateway on the host against the dockerized Postgres. The binary
# serves whatever SPA `make web` last built; run that first to see the UI.
run:
	go run ./cmd/gateway

build: web
	go build ./...

build-go:
	go build ./...

web:
	cd web/dashboard && pnpm install --frozen-lockfile && pnpm run build

# Vite dev server on :5173 with hot reload, proxying /api and /ingest to a
# gateway running on :8080 (see vite.config.ts). Run `make run` alongside it.
web-dev:
	cd web/dashboard && pnpm run dev

tidy:
	go mod tidy

# The CLI is a separate module (its own go.mod, and its own license per BR-53),
# so it is built and tested separately from the gateway.
cli-build:
	cd cli && go build -o bin/whg .

cli-test:
	cd cli && go test -race ./...

# Unit tests only, with the race detector. Integration tests skip themselves
# when TEST_DATABASE_URL is unset, so this stays green without a database.
test:
	go test -race ./...

# Full test suite including the Postgres-backed integration tests. Needs the
# compose Postgres up — this target brings it up itself; TEST_DATABASE_URL
# points the tests at it.
test-integration: db-up
	TEST_DATABASE_URL=$(DATABASE_URL) go test -race -p 1 -count=1 ./...

# Phase 1 end-to-end done-test: boots the gateway against compose Postgres and
# drives a signed + tampered webhook through the real HTTP pipeline.
e2e: db-up
	trap '$(MAKE) db-down' EXIT; ./test/e2e.sh

# Phase 5 done-test: the whole local dev loop — gateway + `whg listen` + a local
# sink — proving a signed webhook comes back out on localhost.
e2e-tunnel: db-up
	trap '$(MAKE) db-down' EXIT; ./test/e2e-tunnel.sh
