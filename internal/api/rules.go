package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/rules"
	"webhook-gateway/internal/tenancy"
)

// RegisterRules mounts the rules CRUD API on mux. A rule is a CEL expression
// evaluated per-source at ingest that can drop an event or override its
// fan-out set. Expressions are compile-checked here so a typo is a
// 400 at write time, not a silently-skipped rule at ingest.
func RegisterRules(mux *http.ServeMux, q *db.Queries, authz *middleware.Auth) {
	h := &rulesHandler{q: q}
	mux.Handle("POST /api/rules", authz.RequireScope(middleware.ScopeWrite, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/rules", authz.RequireScope(middleware.ScopeRead, http.HandlerFunc(h.list)))
	mux.Handle("GET /api/rules/{id}", authz.RequireScope(middleware.ScopeRead, http.HandlerFunc(h.get)))
	mux.Handle("PATCH /api/rules/{id}", authz.RequireScope(middleware.ScopeWrite, http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/rules/{id}", authz.RequireScope(middleware.ScopeWrite, http.HandlerFunc(h.delete)))
}

type rulesHandler struct {
	q *db.Queries
}

type ruleRequest struct {
	SourceID            string   `json:"source_id"`
	Name                string   `json:"name"`
	Expression          string   `json:"expression"`
	Action              string   `json:"action"`
	RouteDestinationIDs []string `json:"route_destination_ids"`
	Priority            *int32   `json:"priority"` // nil = 100
	Enabled             *bool    `json:"enabled"`  // nil = true
}

type ruleResponse struct {
	ID                  string    `json:"id"`
	SourceID            string    `json:"source_id"`
	Name                string    `json:"name"`
	Expression          string    `json:"expression"`
	Action              string    `json:"action"`
	RouteDestinationIDs []string  `json:"route_destination_ids"`
	Priority            int32     `json:"priority"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// validated turns a request into the persisted field set, applying defaults
// and enforcing the invariants the API owns: known action, compilable
// expression, and route destinations present exactly when action=route. The
// source_id is parsed separately since update ignores it.
func (req ruleRequest) validate() (expression, action string, dests []pgtype.UUID, priority int32, enabled bool, problem string) {
	if req.Name == "" {
		return "", "", nil, 0, false, "name is required"
	}
	if req.Expression == "" {
		return "", "", nil, 0, false, "expression is required"
	}
	if err := rules.Validate(req.Expression); err != nil {
		return "", "", nil, 0, false, "expression does not compile: " + err.Error()
	}
	switch req.Action {
	case rules.ActionDrop, rules.ActionRoute:
	default:
		return "", "", nil, 0, false, `action must be "drop" or "route"`
	}

	dests = make([]pgtype.UUID, 0, len(req.RouteDestinationIDs))
	for _, s := range req.RouteDestinationIDs {
		id, err := parseUUID(s)
		if err != nil {
			return "", "", nil, 0, false, "route_destination_ids must all be valid UUIDs"
		}
		dests = append(dests, id)
	}
	if req.Action == rules.ActionRoute && len(dests) == 0 {
		return "", "", nil, 0, false, "route_destination_ids is required when action is route"
	}
	if req.Action == rules.ActionDrop {
		dests = nil // ignored by the drop path; keep the column NULL
	}

	priority = 100
	if req.Priority != nil {
		priority = *req.Priority
	}
	enabled = true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return req.Expression, req.Action, dests, priority, enabled, ""
}

func (h *rulesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	sourceID, err := parseUUID(req.SourceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "source_id must be a valid UUID")
		return
	}
	expression, action, dests, priority, enabled, problem := req.validate()
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}

	rule, err := h.q.InsertRule(r.Context(), db.InsertRuleParams{
		TenantID:            tenancy.DefaultTenantID,
		SourceID:            sourceID,
		Name:                req.Name,
		Expression:          expression,
		Action:              action,
		RouteDestinationIds: dests,
		Priority:            priority,
		Enabled:             enabled,
	})
	if isForeignKeyViolation(err) {
		writeError(w, http.StatusBadRequest, "source_id does not exist")
		return
	}
	if err != nil {
		slog.Error("inserting rule", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, toRuleResponse(rule))
}

func (h *rulesHandler) list(w http.ResponseWriter, r *http.Request) {
	ruleRows, err := h.q.ListRules(r.Context(), tenancy.DefaultTenantID)
	if err != nil {
		slog.Error("listing rules", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]ruleResponse, len(ruleRows))
	for i, rule := range ruleRows {
		out[i] = toRuleResponse(rule)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *rulesHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := h.q.GetRule(r.Context(), db.GetRuleParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err != nil {
		slog.Error("getting rule", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toRuleResponse(rule))
}

func (h *rulesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	expression, action, dests, priority, enabled, problem := req.validate()
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}

	rule, err := h.q.UpdateRule(r.Context(), db.UpdateRuleParams{
		ID:                  id,
		TenantID:            tenancy.DefaultTenantID,
		Name:                req.Name,
		Expression:          expression,
		Action:              action,
		RouteDestinationIds: dests,
		Priority:            priority,
		Enabled:             enabled,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err != nil {
		slog.Error("updating rule", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toRuleResponse(rule))
}

func (h *rulesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	n, err := h.q.DeleteRule(r.Context(), db.DeleteRuleParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if err != nil {
		slog.Error("deleting rule", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func toRuleResponse(rule db.Rule) ruleResponse {
	dests := make([]string, len(rule.RouteDestinationIds))
	for i, id := range rule.RouteDestinationIds {
		dests[i] = uuidString(id)
	}
	return ruleResponse{
		ID:                  uuidString(rule.ID),
		SourceID:            uuidString(rule.SourceID),
		Name:                rule.Name,
		Expression:          rule.Expression,
		Action:              rule.Action,
		RouteDestinationIDs: dests,
		Priority:            rule.Priority,
		Enabled:             rule.Enabled,
		CreatedAt:           rule.CreatedAt.Time,
		UpdatedAt:           rule.UpdatedAt.Time,
	}
}
