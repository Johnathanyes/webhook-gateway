// Package rules evaluates the declarative per-source rules (BR-14) against an
// event before fan-out. Rules are CEL expressions over the event's parsed
// body, headers, and source metadata; the first matching rule (in priority
// order) decides the outcome: 'drop' stops fan-out entirely, 'route' replaces
// the default route set with an explicit destination list.
//
// Evaluation fails open: a rule whose expression doesn't compile, errors at
// runtime, or doesn't produce a bool is logged and skipped, never blocking
// ingest — a broken rule must not turn into an outage.
package rules

import (
	"log/slog"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/db"
)

// Actions, mirroring the rules.action CHECK constraint.
const (
	ActionDrop  = "drop"
	ActionRoute = "route"
)

// Input is what expressions can see. Body is the parsed JSON body (nil when
// the payload wasn't JSON), Headers the stored raw headers
// (map[string][]string shape, as JSON round-trips them), Source a small
// metadata map (name, provider_type).
type Input struct {
	Body    any
	Headers map[string]any
	Source  map[string]any
}

// Decision is the outcome of evaluating a rule set. The zero value means "no
// rule matched": normal fan-out proceeds.
type Decision struct {
	Action              string // "" | ActionDrop | ActionRoute
	RuleName            string
	RouteDestinationIDs []pgtype.UUID
}

// env is built once: three untyped variables keeps expressions permissive —
// `body.amount > 100`, `headers["X-Kind"][0] == "x"`, `source.name == "stripe"`.
var env = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("body", cel.DynType),
		cel.Variable("headers", cel.DynType),
		cel.Variable("source", cel.DynType),
	)
})

// Validate reports whether expression compiles against the rule environment,
// so the CRUD API can reject a typo at write time (400) instead of letting it
// fail open silently at ingest. Returns nil for a valid expression.
func Validate(expression string) error {
	celEnv, err := env()
	if err != nil {
		return err
	}
	if _, iss := celEnv.Compile(expression); iss != nil && iss.Err() != nil {
		return iss.Err()
	}
	return nil
}

// Evaluate runs rs (already filtered to enabled and sorted by priority — see
// ListEnabledRulesForSource) against in. First match wins. Expressions are
// compiled on every call; at self-hosted rule counts that costs microseconds,
// so no cache until profiling says otherwise.
func Evaluate(rs []db.Rule, in Input) Decision {
	if len(rs) == 0 {
		return Decision{}
	}
	celEnv, err := env()
	if err != nil {
		slog.Error("building CEL environment; skipping all rules", "error", err)
		return Decision{}
	}

	activation := map[string]any{
		"body":    in.Body,
		"headers": in.Headers,
		"source":  in.Source,
	}
	// CEL treats a missing/nil top-level as an error in many expressions;
	// normalize nils to empty maps so `body.x` errors (and fails open) rather
	// than panicking the pipeline.
	if in.Body == nil {
		activation["body"] = map[string]any{}
	}
	if in.Headers == nil {
		activation["headers"] = map[string]any{}
	}
	if in.Source == nil {
		activation["source"] = map[string]any{}
	}

	for _, rule := range rs {
		ast, iss := celEnv.Compile(rule.Expression)
		if iss != nil && iss.Err() != nil {
			slog.Warn("rule expression does not compile; skipping", "rule", rule.Name, "error", iss.Err())
			continue
		}
		prg, err := celEnv.Program(ast)
		if err != nil {
			slog.Warn("building rule program; skipping", "rule", rule.Name, "error", err)
			continue
		}
		out, _, err := prg.Eval(activation)
		if err != nil {
			slog.Warn("evaluating rule; skipping", "rule", rule.Name, "error", err)
			continue
		}
		matched, ok := out.Value().(bool)
		if !ok {
			slog.Warn("rule expression is not boolean; skipping", "rule", rule.Name)
			continue
		}
		if !matched {
			continue
		}
		return Decision{
			Action:              rule.Action,
			RuleName:            rule.Name,
			RouteDestinationIDs: rule.RouteDestinationIds,
		}
	}
	return Decision{}
}
