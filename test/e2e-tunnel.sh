#!/usr/bin/env bash
#
# Phase 5 end-to-end done-test (Tasks 26–30). Proves the whole local dev loop
# against a running stack: boot Postgres + the gateway, mint a scoped API key,
# log the CLI in, start `whg listen` pointed at a local sink, then
#
#     send a Stripe-signed webhook and assert it comes back out on
#          localhost with its signature intact and the delivery succeeded
#     `whg trigger` a signed sample event with no provider involved
#     `whg replay` a stored event straight to localhost, byte-for-byte
#
# and finally that detaching leaves no dangling tunnel destination.
#
# Usage:  test/e2e-tunnel.sh        (or: make e2e-tunnel)
#
# Requires: docker compose, go, curl, openssl, python3. Uses the same dev
# credentials as the Makefile so `make db-up` and this script agree on Postgres.
set -euo pipefail

cd "$(dirname "$0")/.."

# --- config: matches the Makefile's dev defaults ---
export DATABASE_URL="${DATABASE_URL:-postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-dev-password}"
export ENCRYPTION_KEY="${ENCRYPTION_KEY:-$(printf 'dev-32-byte-encryption-key-00000' | base64)}"
export LOG_FORMAT="${LOG_FORMAT:-text}"
export PORT="${PORT:-8080}"
BASE="http://localhost:${PORT}"
SECRET="whsec_tunnel_e2e_secret"
SINK_PORT="${SINK_PORT:-13000}"
SOURCE_NAME="e2e-tunnel-stripe"

WORKDIR=$(mktemp -d)
# The CLI writes its credentials under XDG_CONFIG_HOME; pointing it at a temp
# directory keeps the developer's real ~/.config/whg untouched.
export XDG_CONFIG_HOME="${WORKDIR}/xdg"

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$1"; exit 1; }

psql_q() { docker compose exec -T postgres psql -U gateway -d gateway -tAc "$1"; }

# sink_field <line-index> <field> — read one field from the Nth request the
# local sink recorded, so each stage can assert on its own delivery.
sink_field() {
  python3 -c 'import json,sys; print(json.loads(open(sys.argv[1]).readlines()[int(sys.argv[2])])[sys.argv[3]])' \
    "$RECEIVED" "$1" "$2"
}

# sink_count — how many requests the local sink has received so far.
sink_count() { wc -l <"$RECEIVED" | tr -d '[:space:]'; }

# wait_for_sink <n> — block until the sink has recorded at least n requests.
wait_for_sink() {
  for _ in $(seq 1 60); do
    [ "$(sink_count)" -ge "$1" ] && return 0
    sleep 0.25
  done
  return 1
}

# --- bring up dependencies ---
echo "==> starting Postgres"
docker compose up -d --wait >/dev/null

echo "==> building CLI"
(cd cli && go build -o bin/whg .)
WHG="./cli/bin/whg"

echo "==> building gateway"
go build -o "${WORKDIR}/gateway" ./cmd/gateway

echo "==> starting gateway"
"${WORKDIR}/gateway" >"${WORKDIR}/gateway.log" 2>&1 &
GATEWAY_PID=$!

cleanup() {
  kill "${LISTEN_PID:-}" >/dev/null 2>&1 || true
  kill "${SINK_PID:-}" >/dev/null 2>&1 || true
  [ -n "${SOURCE_PATH:-}" ] && psql_q "
    DELETE FROM events WHERE source_id = (SELECT id FROM sources WHERE endpoint_path='${SOURCE_PATH}');
    DELETE FROM sources WHERE endpoint_path='${SOURCE_PATH}';
    DELETE FROM api_keys WHERE name='e2e-tunnel-key';" >/dev/null 2>&1 || true
  kill "$GATEWAY_PID" >/dev/null 2>&1 || true
  # Reap the backgrounded jobs so bash doesn't print "Terminated" after the
  # final PASS line, which reads like a failure.
  wait "$GATEWAY_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for _ in $(seq 1 40); do
  curl -fsS "${BASE}/health" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS "${BASE}/health" >/dev/null || fail "gateway never became healthy (see ${WORKDIR}/gateway.log)"
pass "gateway healthy"

# --- a local sink that records what it receives ---
cat >"${WORKDIR}/sink.py" <<'PY'
import http.server, json, sys, threading

received = []

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        with open(sys.argv[2], 'a') as f:
            f.write(json.dumps({
                'body': body.decode('utf-8', 'replace'),
                'stripe_signature': self.headers.get('Stripe-Signature', ''),
                'webhook_id': self.headers.get('Webhook-Id', ''),
            }) + "\n")
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'ok')

    def log_message(self, *args):
        pass

http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), Handler).serve_forever()
PY

RECEIVED="${WORKDIR}/received.jsonl"
: >"$RECEIVED"
python3 "${WORKDIR}/sink.py" "$SINK_PORT" "$RECEIVED" &
SINK_PID=$!
for _ in $(seq 1 20); do
  curl -fsS -X POST "http://127.0.0.1:${SINK_PORT}/" -d '{}' >/dev/null 2>&1 && break
  sleep 0.25
done
: >"$RECEIVED"  # discard the readiness probe
pass "local sink listening on 127.0.0.1:${SINK_PORT}"

# --- create a Stripe source ---
CREATE_RESP=$(curl -fsS -X POST "${BASE}/api/sources" \
  -H "Authorization: Bearer ${ADMIN_PASSWORD}" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"${SOURCE_NAME}\",\"provider_type\":\"stripe\",\"signing_secret\":\"${SECRET}\"}")
SOURCE_PATH=$(printf '%s' "$CREATE_RESP" | python3 -c 'import sys,json; print(json.load(sys.stdin)["endpoint_path"])')
[ -n "$SOURCE_PATH" ] || fail "could not parse endpoint_path from: $CREATE_RESP"
pass "source created: ${SOURCE_NAME} (${SOURCE_PATH})"

# --- mint a scoped API key and log the CLI in ---
API_KEY=$(curl -fsS -X POST "${BASE}/api/api-keys" \
  -H "Authorization: Bearer ${ADMIN_PASSWORD}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"e2e-tunnel-key","scopes":["read","write","tunnel"]}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["key"])')
[ -n "$API_KEY" ] || fail "could not mint an API key"

$WHG login --url "$BASE" --api-key "$API_KEY" >/dev/null || fail "whg login failed"
pass "whg login round-tripped an authenticated GET /api/sources"

# --- attach the tunnel ---
$WHG listen --source "$SOURCE_NAME" --forward-to "127.0.0.1:${SINK_PORT}" \
  >"${WORKDIR}/listen.log" 2>&1 &
LISTEN_PID=$!

for _ in $(seq 1 40); do
  grep -q "tunnel ready" "${WORKDIR}/listen.log" 2>/dev/null && break
  sleep 0.25
done
grep -q "tunnel ready" "${WORKDIR}/listen.log" \
  || fail "whg listen never became ready: $(cat "${WORKDIR}/listen.log")"
pass "whg listen attached"

# --- send a validly Stripe-signed webhook to the gateway ---
# MARKER rides inside the payload so the log check at the end can prove no
# payload content reached the gateway's or the CLI's log (task #38).
MARKER="MARKERPAYLOAD_tunnel_must_never_be_logged_6b52"
BODY="{\"id\":\"evt_tunnel_e2e\",\"type\":\"payment_intent.succeeded\",\"marker\":\"${MARKER}\"}"
TS=$(date +%s)
SIG=$(printf '%s' "${TS}.${BODY}" | openssl dgst -sha256 -hmac "${SECRET}" | awk '{print $NF}')
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/ingest/${SOURCE_PATH}" \
  -H 'Content-Type: application/json' \
  -H "Stripe-Signature: t=${TS},v1=${SIG}" \
  -d "${BODY}")
[ "$CODE" = "200" ] || fail "signed request returned ${CODE}, want 200"
pass "signed webhook accepted by the gateway"

# --- it should come back out on localhost ---
for _ in $(seq 1 60); do
  [ -s "$RECEIVED" ] && break
  sleep 0.25
done
[ -s "$RECEIVED" ] || fail "no event reached the local sink within 15s: $(cat "${WORKDIR}/listen.log")"

GOT_BODY=$(sink_field 0 body)
[ "$GOT_BODY" = "$BODY" ] || fail "sink body mismatch:\n  got:  ${GOT_BODY}\n  want: ${BODY}"
pass "event reached localhost with a byte-exact body"

GOT_SIG=$(sink_field 0 stripe_signature)
[ "$GOT_SIG" = "t=${TS},v1=${SIG}" ] || fail "Stripe-Signature not preserved: got '${GOT_SIG}'"
pass "provider signature header preserved (local verification would pass)"

GOT_ID=$(sink_field 0 webhook_id)
[ -n "$GOT_ID" ] || fail "Webhook-Id header missing"
pass "Webhook-Id set, matching HTTP dispatch"

# --- the ack should have driven the delivery to succeeded ---
for _ in $(seq 1 40); do
  STATUS=$(psql_q "SELECT status FROM deliveries WHERE id = '${GOT_ID}'" | tr -d '[:space:]')
  [ "$STATUS" = "succeeded" ] && break
  sleep 0.25
done
[ "$STATUS" = "succeeded" ] || fail "delivery ${GOT_ID} status is '${STATUS}', want succeeded"
pass "ack recorded: delivery marked succeeded"

grep -qE "${SOURCE_NAME} payment_intent\.succeeded → 200" "${WORKDIR}/listen.log" \
  || fail "expected log line missing:\n$(cat "${WORKDIR}/listen.log")"
pass "listen logged one readable line per event"

# --- Task 30: `whg trigger` + `whg listen` complete the loop with no provider ---
TRIGGER_OUT=$($WHG trigger --source "$SOURCE_NAME") || fail "whg trigger failed: ${TRIGGER_OUT}"
printf '%s' "$TRIGGER_OUT" | grep -q "verified: true" \
  || fail "triggered sample event was not verified:\n${TRIGGER_OUT}"
pass "whg trigger sent a signed sample event through real verification"

TRIGGERED_EVENT_ID=$(printf '%s' "$TRIGGER_OUT" | sed -n 's/^Event \([0-9a-f-]*\) stored.*/\1/p')
[ -n "$TRIGGERED_EVENT_ID" ] || fail "could not parse event id from:\n${TRIGGER_OUT}"

wait_for_sink 2 || fail "triggered event never reached localhost:\n$(cat "${WORKDIR}/listen.log")"
TRIGGER_BODY=$(sink_field 1 body)
printf '%s' "$TRIGGER_BODY" | grep -q '"type": "payment_intent.succeeded"' \
  || fail "sample payload not delivered to localhost: ${TRIGGER_BODY}"
pass "triggered event delivered to localhost via the tunnel"

TRIGGER_SIG=$(sink_field 1 stripe_signature)
[ -n "$TRIGGER_SIG" ] || fail "triggered event arrived without a Stripe-Signature header"
pass "triggered event carried a real generated signature"

# --- Task 29: `whg replay` re-sends a stored event straight to localhost ---
# Replay does not use the tunnel, so detach first to prove that.
kill "$LISTEN_PID" >/dev/null 2>&1 || true
wait "$LISTEN_PID" 2>/dev/null || true
LISTEN_PID=""

REPLAY_OUT=$($WHG replay "$TRIGGERED_EVENT_ID" --forward-to "127.0.0.1:${SINK_PORT}") \
  || fail "whg replay failed: ${REPLAY_OUT}"
printf '%s' "$REPLAY_OUT" | grep -qE "${SOURCE_NAME} payment_intent\.succeeded → 200" \
  || fail "replay did not report a successful local POST:\n${REPLAY_OUT}"
pass "whg replay re-sent a stored event with the tunnel detached"

wait_for_sink 3 || fail "replayed event never reached the local sink"
REPLAY_BODY=$(sink_field 2 body)
[ "$REPLAY_BODY" = "$TRIGGER_BODY" ] \
  || fail "replayed body is not byte-equal to the stored event:\n  got:  ${REPLAY_BODY}\n  want: ${TRIGGER_BODY}"
pass "replayed body byte-equal to the stored event"

REPLAY_SIG=$(sink_field 2 stripe_signature)
[ "$REPLAY_SIG" = "$TRIGGER_SIG" ] \
  || fail "replayed Stripe-Signature differs:\n  got:  ${REPLAY_SIG}\n  want: ${TRIGGER_SIG}"
pass "replayed event preserved the original provider headers"

# --last N --source <name> selects without needing an id.
LAST_OUT=$($WHG replay --last 1 --source "$SOURCE_NAME" --forward-to "127.0.0.1:${SINK_PORT}") \
  || fail "whg replay --last failed: ${LAST_OUT}"
wait_for_sink 4 || fail "--last replay never reached the local sink"
pass "whg replay --last 1 --source resolved and replayed by name"

# --- the tunnel detached above must have left no dangling destination ---
for _ in $(seq 1 40); do
  LEFTOVER=$(psql_q "SELECT count(*) FROM destinations WHERE url LIKE 'tunnel://%'" | tr -d '[:space:]')
  [ "$LEFTOVER" = "0" ] && break
  sleep 0.25
done
[ "$LEFTOVER" = "0" ] || fail "${LEFTOVER} tunnel destination(s) left behind after disconnect"
pass "disconnect cleaned up the ephemeral destination"

# Neither the signing secret nor payload content may
# appear in any log this run produced — the gateway's, or the CLI's, which
# handled the same payload on its way to localhost.
for logfile in "${WORKDIR}/gateway.log" "${WORKDIR}/listen.log"; do
  [ -f "$logfile" ] || continue
  if grep -q "$SECRET" "$logfile"; then
    fail "signing secret found in $(basename "$logfile"):
$(grep -n "$SECRET" "$logfile" | head -5)"
  fi
  if grep -q "$MARKER" "$logfile"; then
    fail "payload content found in $(basename "$logfile"):
$(grep -n "$MARKER" "$logfile" | head -5)"
  fi
done
pass "no signing secret or payload content in the gateway or CLI logs"

echo "==> PASS: tunnel end-to-end dev loop verified"
