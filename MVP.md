# Open-Source Inbound Webhook Gateway — Fleshed-Out Plan

Working model: true open-source core + paid managed cloud. Solo founder, Go backend, long-term horizon.

---

## 1. Business requirements (exhaustive)

### 1.1 Product requirements — core (v1, must-have)

**Ingestion**

- BR-01: Accept webhooks over HTTPS on per-source endpoint URLs (unique, unguessable paths).
- BR-02: Signature verification built in for an initial catalog of ~15 providers (Stripe, Shopify, GitHub, GitLab, Clerk, Twilio, SendGrid, Paddle, Lemon Squeezy, PayPal, Slack, Linear, Vercel, Resend, generic HMAC).
- BR-03: Generic source type (custom HMAC / basic auth / API key / none) for any provider not in the catalog.
- BR-04: Accept JSON, form-encoded, and raw/XML bodies; store raw payload + headers verbatim.
- BR-05: Respond 2xx to the provider immediately after durable persistence (ack-then-process), within provider timeout windows (<5s).
- BR-06: Ingest-side rate limiting and max payload size to prevent abuse.

**Queueing & delivery**

- BR-07: Durable at-least-once delivery queue; events survive process restarts.
- BR-08: Retries with exponential backoff + jitter; configurable attempts (default ~8 over 3 days).
- BR-09: Dead-letter state for exhausted events, with manual and bulk recovery.
- BR-10: Per-destination rate limiting / delivery pacing (backpressure) so a slow consumer never gets overwhelmed.
- BR-11: Configurable delivery timeout; treat slow endpoints as failures and retry.
- BR-12: Pause/resume delivery per destination (deploy windows, incident response).

**Processing**

- BR-13: Deduplication — exact payload match and field-based (e.g., dedupe on `event.id`) within a configurable window.
- BR-14: Filtering — declarative rules on payload fields, headers, and source (drop or route).
- BR-15: Fan-out — one source to N destinations; N sources to one destination.

**Observability & recovery**

- BR-16: Event log with status, attempts, timing; full-text + field search.
- BR-17: Per-event trace view: received → verified → queued → attempts → outcome, with request/response bodies.
- BR-18: One-click replay of any event; bulk replay by filter (time range, status, source).
- BR-19: Alerting on failure-rate thresholds and dead-lettered events (email, Slack webhook v1; PagerDuty v2).
- BR-20: Prometheus `/metrics` endpoint; health/readiness endpoints.

**Developer experience**

- BR-21: CLI that tunnels events to localhost for development (the free adoption hook), including replay-to-localhost.
- BR-22: Test-event generator per source type.
- BR-23: Quickstart: Docker Compose to first event in under 10 minutes; no SDK required (pure HTTP).
- BR-24: Full REST API for everything the UI does; API keys with scopes.

**Administration & security**

- BR-25: Web UI for source/destination/rule CRUD, event browsing, replay.
- BR-26: Secrets (signing keys) encrypted at rest; TLS everywhere; no payload contents in logs.
- BR-27: Single-user auth in OSS v1 (admin password / OIDC-ready design); multi-user is a v2/paid concern.
- BR-28: Versioned config export/import (foundation for config-as-code in v2).

**Deployment (OSS)**

- BR-29: Single static binary and a single Docker image; Postgres as the ONLY hard dependency (no Redis/Kafka).
- BR-30: Automatic schema migrations on upgrade; documented backup/restore path.
- BR-31: Runs comfortably on a $5 VPS at hobby scale; documented resource envelope.

### 1.2 Product requirements — v2 (paid-tier and depth)

- BR-32: Transformations (JS or CEL expressions) to reshape payloads before delivery.
- BR-33: Config-as-code: YAML definitions + CLI apply; Terraform provider later.
- BR-34: Environments (dev/staging/prod) with promotion.
- BR-35: Multi-user, roles/RBAC, audit log (cloud/paid).
- BR-36: Traffic analytics: provider health, failure/latency dashboards, anomaly alerts.
- BR-37: Community-extensible source catalog: a declarative source-definition format so providers can be added by PR without touching core code. Target 60+ sources.
- BR-38: Scheduled/delayed delivery and cron-triggered synthetic events (light jobs adjacency).

### 1.3 Cloud/SaaS operational requirements

- BR-39: Multi-tenant isolation (tenant-scoped data, keys, quotas); noisy-neighbor protection.
- BR-40: Usage metering (events ingested + delivered) wired to billing; hard/soft quota enforcement with grace behavior (queue, don't drop, on soft overage).
- BR-41: 99.9% uptime target for ingest; ingest path isolated from dashboard path so UI outages never lose events.
- BR-42: Public status page (dogfood: run it on your own product where possible).
- BR-43: Backups + tested restore; retention enforcement per plan tier.
- BR-44: Abuse prevention: open ingest endpoints attract spam/DDoS — WAF/CDN in front, per-endpoint rate limits, payload caps, automatic endpoint rotation.
- BR-45: Single region at launch (choose US-East or EU-Central based on early users); EU data-residency option is a known future request — architect for it, don't build it.
- BR-46: On-call reality: as a solo operator, define what "incident response" means (alerting to your phone, documented runbooks, honest SLA language).

### 1.4 Legal, financial & company requirements

- BR-47: Entity (LLC or equivalent), business bank account, bookkeeping from day one.
- BR-48: Merchant of record decision: Stripe (you handle sales tax/VAT) vs Paddle/Lemon Squeezy (they handle it, higher fees). Recommendation for solo: MoR (Paddle/LS) until >$20–30k MRR, then revisit.
- BR-49: Terms of service, privacy policy, DPA template (customers will ask — you are a data processor handling their event payloads, which may contain PII).
- BR-50: GDPR posture: retention controls, deletion API, subprocessor list. Not SOC 2 at launch; keep an audit trail of practices so SOC 2 is achievable in year 2–3 if enterprise pull appears.
- BR-51: License choice (see §4 pricing — it's a business decision): recommendation AGPL-3.0 for the server (blocks cloud-clone capture, Plausible-style), MIT/Apache-2.0 for CLI + client libraries (zero adoption friction).
- BR-52: Trademark the product name; own the domain, GitHub org, Docker Hub org, and social handles before announcing anything.
- BR-53: Contributor policy: DCO sign-off (lighter than CLA) + clear statement of the open-core boundary to avoid community backlash later.

### 1.5 Community & support requirements

- BR-54: Documentation site: quickstart, per-provider guides, self-hosting ops guide, API reference.
- BR-55: GitHub Discussions or Discord for community; defined response-time expectations (fast responses are a solo superpower — commit to <24h on issues in year 1).
- BR-56: Security disclosure policy (SECURITY.md, security email) — table stakes for infra software.
- BR-57: Public roadmap; monthly release cadence with changelogs (rhythm builds trust).
- BR-58: Opt-in anonymous telemetry (instance count, version, event volume bucket) — your only visibility into self-hosted adoption. Must be transparent and default-visible.

### 1.6 Financial requirements & milestones

- BR-59: 12–18 months personal runway before depending on revenue.
- BR-60: Cash cost budget: ≤$300/mo pre-revenue (infra, domain, email, docs hosting), ≤$1k/mo at first 100 cloud customers.
- BR-61: Milestones: M6 — OSS launched, 1k stars, 5–10 paying; M12 — $3–5k MRR; M18 — $10k MRR (go/no-go for full-time); M36 — $25–40k MRR base case.

---

## 2. Tech stack (Go backend)

Go is the right call — it's exactly what the successful self-hosted infra tools use (Flipt, Gatus, and Hookdeck's own Outpost are Go), and "single static binary" is itself a marketing feature.

### Backend

| Concern | Choice | Why |
| --- | --- | --- |
| Language | Go 1.26+ | Single binary, easy cross-compile, great concurrency for delivery workers |
| HTTP router | `net/http` + stdlib 1.22 routing, or `chi` | Zero/minimal deps; no heavy framework |
| Database | PostgreSQL 18+ | The only hard dependency; battle-tested, self-hosters already run it |
| DB access | `pgx` + `sqlc` | Type-safe generated queries, no ORM magic |
| Job/delivery queue | **River** (riverqueue.com) — Postgres-backed Go queue | Kills the Redis dependency; transactional enqueue with the event write (this is the architectural moat for self-hosting simplicity) |
| Migrations | `goose` or River's + `tern` | Auto-run on startup |
| Signature verification | Per-provider verifiers behind one interface; declarative source-definition format (YAML) driving them | Enables the community catalog (BR-37) |
| Expressions (filters, v2 transforms) | **CEL-Go** (Common Expression Language) | Sandboxed, fast, no JS runtime needed in v1; embed goja (JS) only if v2 demands it |
| Config | env vars + single YAML file | 12-factor, container-friendly |
| Observability | OpenTelemetry SDK, Prometheus exporter, `slog` structured logging | BR-20 |
| Crypto/secrets | `age` or AES-GCM envelope encryption for stored signing secrets | BR-26 |

### Frontend (Landing && Docs)

- Framework: Next.js
- Docs: Mintlify

### Frontend (dashboard)

- **SPA embedded in the Go binary via `go:embed`** — one artifact to deploy, nothing extra for self-hosters.
- Framework: Vite + React. TanStack Query for API state.
- Real-time event tail: SSE (simpler than WebSockets, proxies handle it better).

### CLI

- Go + `cobra`; distributed via Homebrew, `go install`, npm shim, and GitHub Releases (use `goreleaser`).
- Localhost tunnel: outbound WebSocket from CLI to gateway (same pattern as Stripe CLI / Hookdeck CLI) — no ports to open on the dev machine.

### Cloud infrastructure (managed offering)

- Compute: undetermined for now
- Postgres: Neon
- Edge/ingest protection: Cloudflare in front of ingest endpoints (WAF, rate limiting, DDoS) — non-negotiable given BR-44.
- Email: Resend
- Billing: Paddle
- Error tracking: Sentry (free tier); uptime: your own product once stable + one external check.

### Dev & release toolchain

- GitHub (repo, Actions CI, Releases), `goreleaser` (binaries + Docker multi-arch), Docker Hub + GHCR.
- Testing: table-driven unit tests, `testcontainers-go` for Postgres integration tests, `k6` for ingest load tests (publish the benchmark — "X events/sec on a $5 VPS" is marketing).
- Docs: mintlify

### Architecture guardrails

- Ingest path = dumb, fast, isolated: verify → persist → 200. Everything else (delivery, UI, retries) happens async off the queue. If the dashboard dies, no event is ever lost.
- One process by default (self-host), but ingest/worker/UI separable by flag for cloud scaling.
- Design tenant_id into every table from day one even though OSS runs single-tenant — retrofitting multi-tenancy is misery.

---

## 3. Marketing plan

Reality check: Hookdeck owns the search results in this niche. You will not win on SEO head-to-head in year 1. Your channel is GitHub-native distribution, the way Uptime Kuma won monitoring. SEO is a year-2 compounding bet via provider guides.

### Phase 0 — Pre-launch (months 0–3, while building)

1. Secure name/domain/orgs (BR-52). Name should be short, CLI-friendly, and searchable.
2. Landing page with waitlist + a one-paragraph manifesto ("webhook infrastructure should be open source").
3. Build in public: weekly progress posts on X/Bluesky + dev.to; devlog issues in the repo. Goal: 200–500 followers who care before launch day.
4. Ethical Convoy outreach: monitor Convoy's GitHub issues/Discord for people asking about maintenance status; be helpful, mention what you're building when relevant, and build a migration guide + import tool from Convoy configs. These are your first 20 users.
5. Write the launch assets in advance: killer README (GIF of the event trace UI, 10-minute quickstart), architecture blog post, "why open source" post.

### Phase 1 — OSS launch (month ~4)

1. **Show HN** — the single highest-leverage event. Title formula that works: "Show HN: X — open-source, self-hostable webhook gateway (Hookdeck alternative)". Ship the day you can survive the traffic; answer every comment for 12 hours.
2. Same week: r/selfhosted, r/golang, r/webdev, Lobsters; PRs to awesome-selfhosted, awesome-go, openalternative.co, selfh.st.
3. Convoy migration guide published and pinned.
4. Success bar: 500–1,500 stars in week one, 50+ Discord/Discussions members, 10+ real deployments giving feedback.

### Phase 2 — Cloud launch (months 6–9)

1. Launch managed cloud to the OSS user base first (email waitlist + in-app notice in the OSS dashboard: "Don't want to run this yourself?").
2. Product Hunt launch for the cloud (PH works better for SaaS than for OSS repos).
3. Publish transparent pricing + a public comparison table vs Hookdeck/Svix pricing (undercutting Svix's $490/mo cliff is a story in itself).
4. Start a public dashboard of your own MRR/usage if comfortable — build-in-public compounding.

### Phase 3 — Content engine (months 6–24, ongoing)

The SEO moat that matches your product structure: **one guide per provider**. "How to receive Stripe webhooks reliably (in Go/Node/Python)", "Verify Shopify webhook signatures", "Handle GitHub webhook retries" — 60+ providers × high-intent long-tail queries that Hookdeck only partially covers and that map 1:1 to your source catalog. Each guide ends with "or skip all this with [product]".

- Cadence: 2 provider guides + 1 educational post per month (sustainable solo).
- Educational pillar pieces: "Webhook reliability patterns", "Idempotency for webhooks", "Standard Webhooks explained" — link magnets.
- Comparison pages: "Convoy alternative", "self-hosted Hookdeck alternative", "Svix Ingest alternative" — low volume but 100% buying intent.

### Phase 4 — Community & ecosystem (ongoing)

- Fast issue response (<24h) as an explicit, advertised policy — the thing funded competitors can't match.
- Community source catalog: label `good-first-source` issues; celebrate contributors in release notes.
- Partnerships, not competition: integration guides + mutual docs listings with Trigger.dev, Inngest, Supabase, Railway — you're their ingest layer, they're your execution layer.
- Conference/podcast layer in year 2: Go podcasts, Self-Hosted show, Console.dev newsletter, TLDR dev.

### Channel budget & metrics

- $0 paid ads. Time budget ~30% of working hours post-launch.
- North-star metrics: weekly active instances (telemetry), cloud signups/week, self-host→cloud conversion rate, MRR. Vanity-but-useful: stars, Docker pulls.

---

## 4. Pricing plan

### Principles

1. **Meter events, not seats** for the core (usage aligns price with value); seats/roles only gate team features.
2. **Predictable and public** — the enemy is Svix's free→$490 cliff and enterprise "contact us" opacity. Publish everything.
3. **OSS parity promise**: the self-hosted core is production-complete forever. Paid = (a) we run it for you, (b) team/governance features, (c) long retention. Never gate reliability features (retries, dedup, replay) — that would poison community trust.
4. **Generous free tier** — free users are your distribution.

### Cloud tiers

|  | Free | Starter — $29/mo | Team — $99/mo | Scale — $299+/mo |
| --- | --- | --- | --- | --- |
| Events/mo included | 10k | 250k | 1M | 5M |
| Overage | hard cap (queued, notified) | $4 per additional 1M | $4/M | $3/M, volume discounts |
| Retention | 3 days | 15 days | 30 days | 90 days (configurable) |
| Sources/destinations | 3 | Unlimited | Unlimited | Unlimited |
| Seats | 1 | 3 | 10 | Unlimited |
| Features | Core + CLI | + Slack alerts, longer search | + RBAC, audit log, environments | + SSO/SAML, priority support, DPA/SLA 99.9% |
| Support | Community | Email (48h) | Email (24h) | Priority (8h) + shared Slack |

Positioning math: at 1M events/mo you're $99 vs Hookdeck's comparable spend and vs Svix Ingest's $490 entry — and the buyer can always self-host for $0 + a VPS, which paradoxically *increases* willingness to pay (no lock-in fear).

### Self-hosted monetization (later, optional)

- Year 2+: paid "Enterprise self-hosted" license unlocking SSO/SAML, RBAC, audit log in on-prem installs (~$3–6k/yr) + support contracts. Only build when ≥3 companies ask.

### License strategy (pricing-adjacent, decide before launch)

- Server: **AGPL-3.0** — anyone can self-host and modify freely; cloud providers can't strip-mine it into a competing managed service without open-sourcing their stack. This is the Plausible/Grafana-style protection a solo founder needs.
- CLI, client helpers, source-definition catalog: **MIT** — zero friction where you want maximum spread.
- Trade-off acknowledged: AGPL scares a small % of corporate adopters. For an infra tool whose buyer is small teams, protection > that loss. Revisit only if enterprise pull is strong.

### Pricing experiments to run

1. Founding-member deal at cloud launch: first 50 customers get Starter at $19/mo forever — urgency + testimonials.
2. Annual = 2 months free (cash-flow smoothing for a solo business).
3. Watch the free→Starter conversion trigger: if most conversions happen at the retention limit rather than the event cap, retention is your real lever — tune it, not event counts.

---

## Open decisions for the next refinement pass

1. Product name + domain (blocks everything in Phase 0).
2. Beachhead persona: stranded Convoy users vs Stripe-first indie SaaS (changes the first 5 source integrations and the launch messaging).
3. AGPL confirmation (affects contributor policy and some adopters).
4. Launch region for cloud (US vs EU — EU has a mild tailwind: data-residency demand and less competition attention).
5. SQLite support for hobbyist installs: adoption boost vs. dual-database maintenance cost (lean: Postgres-only v1, revisit on demand).