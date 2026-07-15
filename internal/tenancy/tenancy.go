// Package tenancy holds the single-tenant assumptions of OSS v1. Every table
// carries tenant_id from day one (so cloud multi-tenancy is a config change,
// not a migration), but self-hosted installs only ever use the one seeded
// tenant.
package tenancy

import "github.com/jackc/pgx/v5/pgtype"

// DefaultTenantID is the tenant seeded by migration 00001
// (00000000-0000-0000-0000-000000000001) — 16 zero bytes with a trailing 1.
// Every source and event created in OSS hangs off it.
var DefaultTenantID = pgtype.UUID{Bytes: [16]byte{15: 1}, Valid: true}
