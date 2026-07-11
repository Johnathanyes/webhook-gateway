# Local development tasks. These are thin wrappers around the real commands —
# read the recipe to see exactly what each one runs.
#

# Dev-only config, matching compose.yaml's Postgres. Never used in production.
# of these inline, e.g. `PORT=9090 make run`.
export DATABASE_URL   ?= postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable
export ADMIN_PASSWORD ?= dev-password
export ENCRYPTION_KEY ?= $(shell printf 'dev-32-byte-encryption-key-00000' | base64)
export LOG_FORMAT ?= text

.PHONY: db-up db-down db-reset run build tidy

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
