#!/usr/bin/env bash
#
# Usage:  test/e2e.sh        (or: make e2e)
#
# Requires: docker compose, go, curl, openssl. Uses the same dev credentials as
# the Makefile so `make db-up` and this script agree on Postgres.
set -euo pipefail

cd "$(dirname "$0")/.."

# --- config: matches the Makefile's dev defaults ---
export DATABASE_URL="${DATABASE_URL:-postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-dev-password}"
export ENCRYPTION_KEY="${ENCRYPTION_KEY:-$(printf 'dev-32-byte-encryption-key-00000' | base64)}"
export LOG_FORMAT="${LOG_FORMAT:-text}"
export PORT="${PORT:-8080}"
BASE="http://localhost:${PORT}"
SECRET="whsec_e2e_test_secret"

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$1"; exit 1; }

# psql inside the compose Postgres container — no local psql needed.
psql_q() { docker compose exec -T postgres psql -U gateway -d gateway -tAc "$1"; }

# --- bring up dependencies and the gateway ---
echo "==> starting Postgres"
docker compose up -d --wait >/dev/null

echo "==> building gateway"
BINDIR=$(mktemp -d)
go build -o "${BINDIR}/gateway" ./cmd/gateway

echo "==> starting gateway"
"${BINDIR}/gateway" >/tmp/gateway-e2e.log 2>&1 &
GATEWAY_PID=$!

cleanup() {
  [ -n "${SOURCE_PATH:-}" ] && psql_q \
    "DELETE FROM events WHERE source_id = (SELECT id FROM sources WHERE endpoint_path='${SOURCE_PATH}');
     DELETE FROM sources WHERE endpoint_path='${SOURCE_PATH}';" >/dev/null 2>&1 || true
  kill "$GATEWAY_PID" >/dev/null 2>&1 || true
  wait "$GATEWAY_PID" 2>/dev/null || true
  rm -rf "$BINDIR"
}
trap cleanup EXIT

# Wait for the health endpoint (up to ~10s).
for _ in $(seq 1 20); do
  if curl -fsS "${BASE}/health" >/dev/null 2>&1; then break; fi
  sleep 0.5
done
curl -fsS "${BASE}/health" >/dev/null || fail "gateway never became healthy (see /tmp/gateway-e2e.log)"
pass "gateway healthy"

# --- create a Stripe source ---
CREATE_RESP=$(curl -fsS -X POST "${BASE}/api/sources" \
  -H "Authorization: Bearer ${ADMIN_PASSWORD}" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"e2e-stripe\",\"provider_type\":\"stripe\",\"signing_secret\":\"${SECRET}\"}")
SOURCE_PATH=$(printf '%s' "$CREATE_RESP" | sed -n 's/.*"endpoint_path":"\([^"]*\)".*/\1/p')
[ -n "$SOURCE_PATH" ] || fail "could not parse endpoint_path from: $CREATE_RESP"
pass "source created: ${SOURCE_PATH}"

# --- send a validly Stripe-signed webhook ---
# MARKER rides inside the payload so the log check at the end can prove no
# payload content reached the gateway's log (task #38).
MARKER="MARKERPAYLOAD_e2e_must_never_be_logged_4d81"
BODY="{\"id\":\"evt_e2e\",\"object\":\"event\",\"marker\":\"${MARKER}\"}"
TS=$(date +%s)
SIG=$(printf '%s' "${TS}.${BODY}" | openssl dgst -sha256 -hmac "${SECRET}" | awk '{print $NF}')
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/ingest/${SOURCE_PATH}" \
  -H 'Content-Type: application/json' \
  -H "Stripe-Signature: t=${TS},v1=${SIG}" \
  -d "${BODY}")
[ "$CODE" = "200" ] || fail "signed request returned ${CODE}, want 200"
pass "signed webhook accepted (200)"

VERIFIED=$(psql_q "SELECT verified FROM events
  WHERE source_id = (SELECT id FROM sources WHERE endpoint_path='${SOURCE_PATH}')
  ORDER BY received_at DESC LIMIT 1")
[ "$VERIFIED" = "t" ] || fail "signed event stored with verified=${VERIFIED}, want t"
pass "signed event stored verified=true"

# --- send a tampered payload (valid header, mutated body) ---
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/ingest/${SOURCE_PATH}" \
  -H 'Content-Type: application/json' \
  -H "Stripe-Signature: t=${TS},v1=${SIG}" \
  -d '{"id":"evt_forged","object":"event"}')
[ "$CODE" = "200" ] || fail "tampered request returned ${CODE}, want 200 (still stored)"

VERIFIED=$(psql_q "SELECT verified FROM events
  WHERE source_id = (SELECT id FROM sources WHERE endpoint_path='${SOURCE_PATH}')
  ORDER BY received_at DESC LIMIT 1")
[ "$VERIFIED" = "f" ] || fail "tampered event stored with verified=${VERIFIED}, want f"
pass "tampered event stored verified=false"

# Neither the signing secret nor payload content may
# appear in the gateway's log, on any path including the failed verification
# above.
if grep -q "$SECRET" /tmp/gateway-e2e.log; then
  fail "signing secret found in the gateway log:
$(grep -n "$SECRET" /tmp/gateway-e2e.log | head -5)"
fi
if grep -q "$MARKER" /tmp/gateway-e2e.log; then
  fail "payload content found in the gateway log:
$(grep -n "$MARKER" /tmp/gateway-e2e.log | head -5)"
fi
pass "no signing secret or payload content in the gateway log"

echo "==> PASS: Phase 1 end-to-end pipeline verified"
