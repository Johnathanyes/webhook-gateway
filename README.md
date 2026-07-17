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

> **Coming soon.**

The goal is to get from `docker compose up` to your first received webhook in under 10 minutes.

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