# Tunnel protocol (v1)

The tunnel lets a developer receive real webhooks on `localhost` without a
public URL. The CLI (`whg listen`) opens a WebSocket to the gateway; for
as long as that socket is attached, the gateway treats it as a destination and
pushes matching events down it instead of POSTing them over HTTP.

The CLI lives in a separate Go module, so this file — not the Go types — is the
contract between the two. The normative Go definitions mirroring it are in
`internal/tunnel/protocol.go`.

## Connecting

```
GET /api/tunnel?source=<name>
Authorization: Bearer <api key with the `tunnel` scope>
Upgrade: websocket
```

- `source` is a **source name**, not an id, because that is what a developer
  types. Omit it, or pass `all`, to tunnel every source.
- Source names are not unique; the oldest match wins.
- Auth is the standard bearer credential — an API key carrying the `tunnel`
  scope, or the admin password. A key without the scope gets `403`; an unknown
  source gets `404`. Both are plain HTTP errors, sent before the upgrade.

On success the server replies `101` and immediately sends a `ready` frame.

## Frames

Every frame is a single JSON text message with a `type` discriminator. Unknown
frame types are ignored by both sides, so the protocol can gain frames without
breaking older clients.

### `ready` (server → client)

Sent once, first.

```json
{
  "type": "ready",
  "destination_id": "0199e2c1-6d0a-7b3e-9f21-4c8a1b2d3e4f",
  "sources": ["stripe"]
}
```

`destination_id` is the ephemeral destination created for this session — useful
for correlating with the events UI while the tunnel is up.

### `event` (server → client)

One per delivery.

```json
{
  "type": "event",
  "delivery_id": "0199e2c1-7a11-7000-8000-000000000001",
  "event_id": "0199e2c1-79f4-7000-8000-000000000002",
  "source": "stripe",
  "content_type": "application/json",
  "headers": { "Content-Type": ["application/json"], "Stripe-Signature": ["t=..,v1=.."] },
  "body": "eyJpZCI6ImV2dF8xIn0="
}
```

- `body` is base64 (Go renders `[]byte` that way). Decode it and forward the
  bytes verbatim — payloads are not guaranteed to be UTF-8 or JSON.
- `headers` are the **provider's original request headers**, in
  `map[string][]string` form. This is deliberately different from HTTP
  dispatch, which forwards none of them: the point of a tunnel is to exercise a
  local handler exactly as production would, signature headers included.
- `delivery_id` is what the ack must echo back. It is also the `Webhook-Id` the
  HTTP path would have sent.

### `ack` (client → server)

One per event. The gateway blocks the delivery until it arrives.

```json
{ "type": "ack", "delivery_id": "0199e2c1-...", "status_code": 200, "duration_ms": 45 }
```

- `status_code` is what the developer's local handler returned. It is recorded
  on the delivery attempt exactly as an HTTP response status would be: `2xx`
  succeeds, anything else fails.
- Set `error` instead of `status_code` when the client never reached its local
  target at all (connection refused, DNS failure):

  ```json
  { "type": "ack", "delivery_id": "0199e2c1-...", "error": "dial tcp 127.0.0.1:3000: connection refused" }
  ```

- `duration_ms` is informational.

A client must ack every event it receives. If no ack arrives within the
destination's timeout (30s for a tunnel), the delivery is marked failed with
`context deadline exceeded`.

## Lifecycle and semantics

**The tunnel is a destination.** Attaching inserts a real `destinations` row
(`url = "tunnel://<label>"`) plus a `routes` row per selected source. Fan-out,
deliveries, attempt history, and the trace view all work on it with no
special-casing — only the dispatch step differs.

**Disconnect means the destination is gone.** Closing the socket deletes the
destination row, and the cascade takes its routes and any delivery still queued
for it. Nothing is left retrying against a socket that no longer exists — and
the session's delivery history goes with it. Tunnel destinations orphaned by a
crash are swept at boot.

**No retries.** A tunnel destination is created with `max_attempts = 1`. A dev
loop is interactive: if the local handler errors, the developer fixes it and
re-triggers, rather than having the event retried for three days. A failed
tunnel delivery is dead-lettered immediately and visible right away.

**Keepalive.** The server pings every 30 seconds. Standard WebSocket pongs are
handled by the client library; nothing application-level is required.

## Deployment constraint

A WebSocket lives in the memory of the process that accepted it, and the
registry mapping destination → socket is process-local. Tunnel delivery
therefore requires the process holding the socket to also be running the
delivery worker — role `all`, which is the default and what the Docker Compose
quickstart ships.

Under split roles (`ingest` / `dashboard` / `worker` in separate processes) the
worker cannot reach the socket, and tunnel deliveries fail with `tunnel not
connected`. Making tunnels work across processes would need a relay
(Postgres `LISTEN`/`NOTIFY` keyed on destination id); that is deliberately out
of scope for v1, since the tunnel is a local-development feature and
multi-process deployments are a production concern.
