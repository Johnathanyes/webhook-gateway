package rules

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
)

func rule(name, expression, action string, dests ...pgtype.UUID) db.Rule {
	return db.Rule{
		Name:                name,
		Expression:          expression,
		Action:              action,
		RouteDestinationIds: dests,
	}
}

func TestEvaluateNoRules(t *testing.T) {
	d := Evaluate(nil, Input{})
	if d.Action != "" {
		t.Errorf("no rules: got action %q, want none", d.Action)
	}
}

func TestEvaluateDropOnBody(t *testing.T) {
	rs := []db.Rule{rule("big-amounts", `body.amount > 100`, ActionDrop)}

	d := Evaluate(rs, Input{Body: map[string]any{"amount": 200}})
	if d.Action != ActionDrop || d.RuleName != "big-amounts" {
		t.Errorf("matching body: got %+v, want drop by big-amounts", d)
	}

	d = Evaluate(rs, Input{Body: map[string]any{"amount": 50}})
	if d.Action != "" {
		t.Errorf("non-matching body: got action %q, want none", d.Action)
	}
}

func TestEvaluateHeadersAndSource(t *testing.T) {
	rs := []db.Rule{rule("gh-ping", `headers["X-Github-Event"][0] == "ping" && source.provider_type == "github"`, ActionDrop)}
	in := Input{
		Headers: map[string]any{"X-Github-Event": []any{"ping"}},
		Source:  map[string]any{"provider_type": "github"},
	}
	if d := Evaluate(rs, in); d.Action != ActionDrop {
		t.Errorf("headers+source match: got %+v, want drop", d)
	}
}

func TestEvaluateFirstMatchWins(t *testing.T) {
	// Both match; the slice is already priority-ordered, so the first wins.
	rs := []db.Rule{
		rule("first", `true`, ActionDrop),
		rule("second", `true`, ActionDrop),
	}
	if d := Evaluate(rs, Input{}); d.RuleName != "first" {
		t.Errorf("got rule %q, want first", d.RuleName)
	}
}

func TestEvaluateRouteCarriesDestinations(t *testing.T) {
	dest := pgtype.UUID{Bytes: [16]byte{15: 7}, Valid: true}
	rs := []db.Rule{rule("to-slack", `body.kind == "alert"`, ActionRoute, dest)}

	d := Evaluate(rs, Input{Body: map[string]any{"kind": "alert"}})
	if d.Action != ActionRoute {
		t.Fatalf("got action %q, want route", d.Action)
	}
	if len(d.RouteDestinationIDs) != 1 || d.RouteDestinationIDs[0] != dest {
		t.Errorf("got destinations %v, want [%v]", d.RouteDestinationIDs, dest)
	}
}

// Fail-open behaviors: broken rules are skipped, later rules still run, and a
// fully-broken rule set means normal fan-out.
func TestEvaluateFailOpen(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"does not compile", `body.amount >`},
		{"errors at runtime", `body.missing.deeply == 1`},
		{"not a boolean", `body.amount`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := []db.Rule{
				rule("broken", tc.expr, ActionDrop),
				rule("fallback", `true`, ActionDrop),
			}
			in := Input{Body: map[string]any{"amount": 5}}
			if d := Evaluate(rs, in); d.RuleName != "fallback" {
				t.Errorf("broken rule not skipped: got %+v, want fallback match", d)
			}

			// Alone, the broken rule must yield the zero decision.
			if d := Evaluate(rs[:1], in); d.Action != "" {
				t.Errorf("broken rule alone: got action %q, want none", d.Action)
			}
		})
	}
}

func TestEvaluateNilInputs(t *testing.T) {
	// A non-JSON payload means Body is nil; expressions over body then error
	// and fail open rather than crashing.
	rs := []db.Rule{rule("needs-body", `body.amount > 1`, ActionDrop)}
	if d := Evaluate(rs, Input{}); d.Action != "" {
		t.Errorf("nil body: got action %q, want none", d.Action)
	}
}
