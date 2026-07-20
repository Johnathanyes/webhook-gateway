package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

const (
	defaultEventsPageSize = 50
	maxEventsPageSize     = 200
)

var validDeliveryStatuses = map[string]bool{
	"pending": true, "succeeded": true, "failed": true,
	"dead_lettered": true, "paused": true,
}

// Mounts read-only events/observability API on mux
func RegisterEvents(mux *http.ServeMux, q *db.Queries, adminPassword string) {
	h := &eventsHandler{q: q}
	mux.Handle("GET /api/events", auth.AdminOnly(adminPassword, http.HandlerFunc(h.list)))
	mux.Handle("GET /api/events/{id}", auth.AdminOnly(adminPassword, http.HandlerFunc(h.get)))
	mux.Handle("GET /api/events/{id}/trace", auth.AdminOnly(adminPassword, http.HandlerFunc(h.trace)))
}

type eventsHandler struct {
	q *db.Queries
}

// List-view projection: metadata only
type eventSummary struct {
	ID          string    `json:"id"`
	SourceID    string    `json:"source_id"`
	ContentType string    `json:"content_type,omitempty"`
	DedupeKey   string    `json:"dedupe_key,omitempty"`
	Verified    bool      `json:"verified"`
	ReceivedAt  time.Time `json:"received_at"`
}

type listEventsResponse struct {
	Events     []eventSummary `json:"events"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (h *eventsHandler) list(w http.ResponseWriter, r *http.Request) {
	params, errMsg, ok := parseListEventsQuery(r)
	if !ok {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	rows, err := h.q.ListEvents(r.Context(), params)
	if err != nil {
		slog.Error("listing events", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Gets one extra row to learn whether a
	// further page exists without a second query. If it came back, trim it and
	// hand its predecessor's id out as the cursor.
	size := int(params.PageLimit) - 1
	var nextCursor string
	if len(rows) > size {
		rows = rows[:size]
		nextCursor = uuidString(rows[len(rows)-1].ID)
	}

	out := listEventsResponse{Events: make([]eventSummary, len(rows))}
	for i, e := range rows {
		out.Events[i] = eventSummary{
			ID:          uuidString(e.ID),
			SourceID:    uuidString(e.SourceID),
			ContentType: e.ContentType.String,
			DedupeKey:   e.DedupeKey.String,
			Verified:    e.Verified,
			ReceivedAt:  e.ReceivedAt.Time,
		}
	}
	out.NextCursor = nextCursor
	writeJSON(w, http.StatusOK, out)
}

// parseListEventsQuery turns the query string into ListEventsParams, defaulting
// every filter to "not applied". PageLimit is the caller's page size plus one
func parseListEventsQuery(r *http.Request) (db.ListEventsParams, string, bool) {
	q := r.URL.Query()
	params := db.ListEventsParams{TenantID: tenancy.DefaultTenantID}

	if v := q.Get("source_id"); v != "" {
		id, err := parseUUID(v)
		if err != nil {
			return params, "invalid source_id", false
		}
		params.SourceID = id
	}
	if v := q.Get("verified"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return params, "verified must be true or false", false
		}
		params.Verified = pgtype.Bool{Bool: b, Valid: true}
	}
	if v := q.Get("after"); v != "" {
		ts, err := parseTimeParam(v)
		if err != nil {
			return params, "after must be an RFC3339 timestamp", false
		}
		params.After = ts
	}
	if v := q.Get("before"); v != "" {
		ts, err := parseTimeParam(v)
		if err != nil {
			return params, "before must be an RFC3339 timestamp", false
		}
		params.Before = ts
	}
	if v := q.Get("search"); v != "" {
		params.Search = pgtype.Text{String: v, Valid: true}
	}
	if v := q.Get("delivery_status"); v != "" {
		if !validDeliveryStatuses[v] {
			return params, "invalid delivery_status", false
		}
		params.DeliveryStatus = pgtype.Text{String: v, Valid: true}
	}
	if v := q.Get("cursor"); v != "" {
		id, err := parseUUID(v)
		if err != nil {
			return params, "invalid cursor", false
		}
		params.Cursor = id
	}

	size := defaultEventsPageSize
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return params, "limit must be a positive integer", false
		}
		size = min(n, maxEventsPageSize)
	}
	params.PageLimit = int32(size + 1)

	return params, "", true
}

// eventDetail is the full single-event view. RawHeaders/ParsedBody are stored JSONB, surfaced verbatim.
type eventDetail struct {
	ID          string          `json:"id"`
	SourceID    string          `json:"source_id"`
	RawHeaders  json.RawMessage `json:"raw_headers"`
	RawBody     []byte          `json:"raw_body"`
	ContentType string          `json:"content_type,omitempty"`
	ParsedBody  json.RawMessage `json:"parsed_body,omitempty"`
	DedupeKey   string          `json:"dedupe_key,omitempty"`
	Verified    bool            `json:"verified"`
	ReceivedAt  time.Time       `json:"received_at"`
}

func (h *eventsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	e, err := h.q.GetEvent(r.Context(), db.GetEventParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		slog.Error("getting event", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, eventDetail{
		ID:          uuidString(e.ID),
		SourceID:    uuidString(e.SourceID),
		RawHeaders:  json.RawMessage(e.RawHeaders),
		RawBody:     e.RawBody,
		ContentType: e.ContentType.String,
		ParsedBody:  json.RawMessage(e.ParsedBody),
		DedupeKey:   e.DedupeKey.String,
		Verified:    e.Verified,
		ReceivedAt:  e.ReceivedAt.Time,
	})
}

// traceAttempt is one HTTP try in a delivery's timeline. Response
// bodies are already truncated at storage time.
type traceAttempt struct {
	AttemptNumber         int32           `json:"attempt_number"`
	RequestHeaders        json.RawMessage `json:"request_headers,omitempty"`
	ResponseStatusCode    *int32          `json:"response_status_code,omitempty"`
	ResponseHeaders       json.RawMessage `json:"response_headers,omitempty"`
	ResponseBodyTruncated string          `json:"response_body_truncated,omitempty"`
	Error                 string          `json:"error,omitempty"`
	DurationMs            *int32          `json:"duration_ms,omitempty"`
	AttemptedAt           time.Time       `json:"attempted_at"`
}

type traceDelivery struct {
	DeliveryID     string         `json:"delivery_id"`
	DestinationID  string         `json:"destination_id"`
	Status         string         `json:"status"`
	AttemptCount   int32          `json:"attempt_count"`
	QueuedAt       time.Time      `json:"queued_at"`
	DeadLetteredAt *time.Time     `json:"dead_lettered_at,omitempty"`
	Attempts       []traceAttempt `json:"attempts"`
}

// eventTrace is the received -> verified -> queued -> attempts -> outcome timeline.
type eventTrace struct {
	EventID    string          `json:"event_id"`
	ReceivedAt time.Time       `json:"received_at"`
	Verified   bool            `json:"verified"`
	Deliveries []traceDelivery `json:"deliveries"`
}

func (h *eventsHandler) trace(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	ctx := r.Context()

	e, err := h.q.GetEvent(ctx, db.GetEventParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		slog.Error("getting event for trace", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	deliveries, err := h.q.ListEventDeliveries(ctx, db.ListEventDeliveriesParams{EventID: id, TenantID: tenancy.DefaultTenantID})
	if err != nil {
		slog.Error("listing event deliveries", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	attempts, err := h.q.ListEventDeliveryAttempts(ctx, db.ListEventDeliveryAttemptsParams{EventID: id, TenantID: tenancy.DefaultTenantID})
	if err != nil {
		slog.Error("listing event delivery attempts", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Group attempts by delivery. Both queries order by delivery_id then
	// attempt_number, so each group is already in attempt order.
	attemptsByDelivery := make(map[[16]byte][]traceAttempt)
	for _, a := range attempts {
		attemptsByDelivery[a.DeliveryID.Bytes] = append(attemptsByDelivery[a.DeliveryID.Bytes], traceAttempt{
			AttemptNumber:         a.AttemptNumber,
			RequestHeaders:        json.RawMessage(a.RequestHeaders),
			ResponseStatusCode:    pgtypeToInt32Ptr(a.ResponseStatusCode),
			ResponseHeaders:       json.RawMessage(a.ResponseHeaders),
			ResponseBodyTruncated: a.ResponseBodyTruncated.String,
			Error:                 a.Error.String,
			DurationMs:            pgtypeToInt32Ptr(a.DurationMs),
			AttemptedAt:           a.AttemptedAt.Time,
		})
	}

	out := eventTrace{
		EventID:    uuidString(e.ID),
		ReceivedAt: e.ReceivedAt.Time,
		Verified:   e.Verified,
		Deliveries: make([]traceDelivery, len(deliveries)),
	}
	for i, d := range deliveries {
		td := traceDelivery{
			DeliveryID:     uuidString(d.ID),
			DestinationID:  uuidString(d.DestinationID),
			Status:         d.Status,
			AttemptCount:   d.AttemptCount,
			QueuedAt:       d.CreatedAt.Time,
			DeadLetteredAt: pgtimeToPtr(d.DeadLetteredAt),
			Attempts:       attemptsByDelivery[d.ID.Bytes],
		}
		if td.Attempts == nil {
			td.Attempts = []traceAttempt{}
		}
		out.Deliveries[i] = td
	}
	writeJSON(w, http.StatusOK, out)
}

// parseTimeParam accepts an RFC3339 timestamp and returns it as a
// pgtype.Timestamptz suitable for a query filter.
func parseTimeParam(s string) (pgtype.Timestamptz, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}

func pgtimeToPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
