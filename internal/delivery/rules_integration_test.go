package delivery

// Integration proof of the rules stage (BR-14): rules evaluated at ingest can
// drop an event (stored, dropped_reason set, zero deliveries) or override the
// fan-out set with an explicit destination list. No worker runs — the
// assertions are about which delivery rows the ingest tx commits.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

func (h *harness) insertRule(t *testing.T, src db.Source, name, expression, action string, enabled bool, dests ...pgtype.UUID) {
	t.Helper()
	if _, err := h.q.InsertRule(context.Background(), db.InsertRuleParams{
		TenantID:            tenancy.DefaultTenantID,
		SourceID:            src.ID,
		Name:                name,
		Expression:          expression,
		Action:              action,
		RouteDestinationIds: dests,
		Priority:            100,
		Enabled:             enabled,
	}); err != nil {
		t.Fatalf("insert rule %q: %v", name, err)
	}
	// No explicit cleanup: rules cascade with the source's deletion.
}

func droppedReason(t *testing.T, pool *pgxpool.Pool, eventID pgtype.UUID) string {
	t.Helper()
	var r pgtype.Text
	if err := pool.QueryRow(context.Background(),
		"SELECT dropped_reason FROM events WHERE id = $1", eventID).Scan(&r); err != nil {
		t.Fatalf("query dropped_reason: %v", err)
	}
	if !r.Valid {
		return ""
	}
	return r.String
}

func deliveryDestinations(t *testing.T, pool *pgxpool.Pool, eventID pgtype.UUID) []pgtype.UUID {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT destination_id FROM deliveries WHERE event_id = $1", eventID)
	if err != nil {
		t.Fatalf("query delivery destinations: %v", err)
	}
	defer rows.Close()
	var out []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan destination id: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate destinations: %v", err)
	}
	return out
}

func TestPipelineRuleDrop(t *testing.T) {
	h := newHarness(t)
	src := h.createSource(t)
	dest := h.createDestination(t, "http://localhost:1/unreachable", destOpts{})
	h.route(t, src, dest)

	h.insertRule(t, src, "drop-blocked", `body.kind == "blocked"`, "drop", true)

	// Matching event: stored, zero deliveries, dropped_reason names the rule.
	dropped := h.postEvent(t, src, `{"kind":"blocked"}`)
	if got := deliveriesForEvent(t, h.pool, dropped); len(got) != 0 {
		t.Errorf("dropped event has %d deliveries, want 0", len(got))
	}
	if got := droppedReason(t, h.pool, dropped); got != "drop-blocked" {
		t.Errorf("dropped_reason = %q, want drop-blocked", got)
	}

	// Non-matching event: normal fan-out, not marked dropped.
	kept := h.postEvent(t, src, `{"kind":"ok"}`)
	if got := deliveriesForEvent(t, h.pool, kept); len(got) != 1 {
		t.Errorf("kept event has %d deliveries, want 1", len(got))
	}
	if got := droppedReason(t, h.pool, kept); got != "" {
		t.Errorf("kept event dropped_reason = %q, want empty", got)
	}
}

func TestPipelineRuleRouteOverride(t *testing.T) {
	h := newHarness(t)
	src := h.createSource(t)
	routed := h.createDestination(t, "http://localhost:1/routed", destOpts{})
	h.route(t, src, routed)
	// override is NOT routed to the source — only the rule can reach it.
	override := h.createDestination(t, "http://localhost:1/override", destOpts{})

	h.insertRule(t, src, "special-route", `body.kind == "special"`, "route", true, override.ID)

	// Matching event: fan-out replaced by the rule's destination list.
	ev := h.postEvent(t, src, `{"kind":"special"}`)
	dests := deliveryDestinations(t, h.pool, ev)
	if len(dests) != 1 || dests[0] != override.ID {
		t.Errorf("route override delivered to %v, want [%v]", dests, override.ID)
	}

	// Non-matching event: the default routed destination.
	ev = h.postEvent(t, src, `{"kind":"normal"}`)
	dests = deliveryDestinations(t, h.pool, ev)
	if len(dests) != 1 || dests[0] != routed.ID {
		t.Errorf("default fan-out delivered to %v, want [%v]", dests, routed.ID)
	}
}

func TestPipelineRuleFailOpenAndDisabled(t *testing.T) {
	h := newHarness(t)
	src := h.createSource(t)
	dest := h.createDestination(t, "http://localhost:1/unreachable", destOpts{})
	h.route(t, src, dest)

	// A rule that doesn't compile and a disabled always-match drop: neither
	// may stop delivery.
	h.insertRule(t, src, "broken", `body.amount >`, "drop", true)
	h.insertRule(t, src, "disabled-drop", `true`, "drop", false)

	ev := h.postEvent(t, src, `{"amount": 5}`)
	if got := deliveriesForEvent(t, h.pool, ev); len(got) != 1 {
		t.Errorf("event has %d deliveries, want 1 (broken rule must fail open, disabled rule must not run)", len(got))
	}
	if got := droppedReason(t, h.pool, ev); got != "" {
		t.Errorf("dropped_reason = %q, want empty", got)
	}
}
