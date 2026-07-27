# Open-Source Webhook Gateway

>  **Work in Progress**  
> This project is in active development and has **not yet reached v1.0**. APIs, configuration, and features are subject to change. Feedback, feature requests, and contributions are welcome!

Open-source webhook infrastructure that lets you **receive, inspect, replay, and reliably deliver webhooks**—without building the plumbing yourself.

Receive events from providers like Stripe, GitHub, Shopify, Slack, and many more. Verify signatures, store every event durably, retry failed deliveries automatically, replay events with one click, and inspect everything from a clean web dashboard.

Deploy it anywhere with a single Docker container and PostgreSQL.

---

## Why?

Building reliable webhook infrastructure is harder than it looks.

Most applications eventually need:

- Signature verification
- Durable event storage
- Automatic retries
- Dead-letter queues
- Event replay
- Fan-out to multiple services
- Local development tooling
- Searchable event history

Instead of rebuilding this for every project, deploy the gateway and focus on your application.

---

## Features

### Reliable Ingestion

- Accept webhooks over HTTPS
- Durable persistence before acknowledging providers
- Supports JSON, form data, XML, and raw payloads
- Preserves original payloads and headers
- Configurable payload limits
- Built-in rate limiting

### Built-in Signature Verification

Works out of the box with providers such as:

- Stripe
- GitHub
- GitLab
- Shopify
- Clerk
- Twilio
- Slack
- Paddle
- Lemon Squeezy
- PayPal
- Vercel
- Resend
- SendGrid

Also supports:

- Generic HMAC
- API Key authentication
- Basic Authentication
- Custom verification strategies

### Event Processing

- Event deduplication
- Declarative filtering
- Fan-out routing
- Multiple destinations per source

### Observability

- Complete event history
- Request and response inspection
- One-click event replay
- Bulk replay
- Searchable event log
- Prometheus metrics
- Health and readiness endpoints

### Developer Experience

- Localhost tunneling CLI
- Test event generator
- REST API
- Docker deployment
- Single static binary
- PostgreSQL as the only required dependency

---

## Quick Start

From a clean checkout to a **verified, signed webhook delivered end-to-end — in under 10 minutes.** No provider account, no ngrok, no cloud anything. Two containers and curl.

**You need:** Docker (with Compose v2), `curl`, `jq`, and `openssl`.

### 1. Boot the stack

```bash
git clone https://github.com/Johnathanyes/webhook-gateway.git
cd webhook-gateway/deploy/docker

# Generate your credentials. The encryption key protects signing secrets at
# rest — save it somewhere safe; losing it makes stored secrets unreadable.
export ADMIN_PASSWORD="$(openssl rand -base64 18)"
cat > .env <<EOF
ADMIN_PASSWORD=${ADMIN_PASSWORD}
ENCRYPTION_KEY=$(openssl rand -base64 32)
EOF
echo "admin password: ${ADMIN_PASSWORD}"

# --profile demo adds a throwaway sink so you can watch a webhook land.
docker compose --profile demo up -d --wait
```

The first run builds the image (a minute or two); after that it's seconds. `--wait` blocks until the gateway is healthy — migrations run automatically on boot, so there's no separate migrate step, ever.

```bash
export GATEWAY=http://localhost:8080
curl -s $GATEWAY/health
```

### 2. Create a source

A **source** is a provider sending you webhooks. The gateway generates an unguessable endpoint path and encrypts the signing secret before it touches disk.

```bash
export SECRET="whsec_quickstart_secret"

SOURCE=$(curl -fsS -X POST $GATEWAY/api/sources \
  -H "Authorization: Bearer $ADMIN_PASSWORD" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"my-stripe\",\"provider_type\":\"stripe\",\"signing_secret\":\"$SECRET\"}")

export SOURCE_ID=$(echo "$SOURCE" | jq -r .id)
export ENDPOINT=$(echo "$SOURCE" | jq -r .endpoint_path)

echo "Point Stripe at: $GATEWAY/ingest/$ENDPOINT"
```

### 3. Point it at a destination

A **destination** is where events go, and a **route** binds the two. Retries, timeout, and backoff are per-destination policy.

```bash
export DEST_ID=$(curl -fsS -X POST $GATEWAY/api/destinations \
  -H "Authorization: Bearer $ADMIN_PASSWORD" \
  -H 'Content-Type: application/json' \
  -d '{"name":"demo-sink","url":"http://sink:3000","timeout_ms":5000,
       "max_attempts":5,"backoff_base_seconds":2,"backoff_max_seconds":300}' | jq -r .id)

curl -fsS -X POST $GATEWAY/api/routes \
  -H "Authorization: Bearer $ADMIN_PASSWORD" \
  -H 'Content-Type: application/json' \
  -d "{\"source_id\":\"$SOURCE_ID\",\"destination_id\":\"$DEST_ID\"}" | jq
```

> `http://sink:3000` is the demo container. Swap in your own service's URL for anything real.

### 4. Send your first event

You don't need a Stripe account to test Stripe. The gateway signs a real sample payload with **your** source's secret and pushes it through the **actual** ingest path — signature verification included, not bypassed:

```bash
export EVENT_ID=$(curl -fsS -X POST $GATEWAY/api/sources/$SOURCE_ID/test-event \
  -H "Authorization: Bearer $ADMIN_PASSWORD" | tee /dev/stderr | jq -r .event_id)
```

```json
{ "event_id": "0198...", "verified": true }
```

Prefer to prove it the hard way? Sign a payload yourself, exactly as Stripe would:

```bash
BODY='{"id":"evt_quickstart","type":"payment_intent.succeeded"}'
TS=$(date +%s)
SIG=$(printf '%s' "$TS.$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')

curl -i -X POST $GATEWAY/ingest/$ENDPOINT \
  -H 'Content-Type: application/json' \
  -H "Stripe-Signature: t=$TS,v1=$SIG" \
  -d "$BODY"
```

Change one byte of that body and the event is still stored — flagged `verified: false`, never silently dropped.

### 5. Watch it get delivered

```bash
docker compose logs sink            # the raw payload, as your service received it
```

Then the full audit trail — received → verified → queued → every attempt with status code and duration:

```bash
curl -fsS $GATEWAY/api/events/$EVENT_ID/trace \
  -H "Authorization: Bearer $ADMIN_PASSWORD" | jq
```

Every event is searchable, and any of them can be replayed:

```bash
curl -fsS "$GATEWAY/api/events?search=payment_intent" \
  -H "Authorization: Bearer $ADMIN_PASSWORD" | jq '.events[].id'

curl -fsS -X POST $GATEWAY/api/events/$EVENT_ID/replay \
  -H "Authorization: Bearer $ADMIN_PASSWORD" | jq
```

**That's the whole loop.** Received, verified, stored, routed, delivered, traced, replayed.

### 6. Now do it on localhost

The CLI tunnels live events straight to your dev server — no public URL:

```bash
cd ../../cli && go build -o bin/whg .

./bin/whg login --url http://localhost:8080 --api-key <key>   # POST /api/api-keys to mint one
./bin/whg listen --source my-stripe --forward-to localhost:3000
./bin/whg trigger --source my-stripe    # in another shell
```

```
12:04:31  my-stripe payment_intent.succeeded → 200 (45ms)
```

### Tear it down

```bash
cd deploy/docker
docker compose --profile demo down -v   # -v also drops the database volume
```

---

## Documentation

Documentation is currently being written alongside development.

Planned guides include:

- Installation
- Quick Start
- Provider Guides
- Self-Hosting
- REST API
- CLI Reference

---

## License

**Server:** AGPL-3.0

**CLI and client libraries:** MIT

---

## Contributing

Contributions are welcome!

Whether you're fixing a bug, improving documentation, adding support for another webhook provider, or suggesting new features, we'd love your help.

If you're interested in contributing, feel free to open an issue to discuss ideas before submitting a pull request.

---

## Support the Project

If you find this project interesting, consider giving it a star on GitHub.