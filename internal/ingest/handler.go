package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/observability"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/rules"
	"webhook-gateway/internal/sourcedef"
)

type Options struct {
	MaxBodyBytes       int64 // reject bodies larger than this with 413
	RateLimitPerSecond int   // per-source token-bucket rate; also the burst
}

// Handler stores inbound webhooks.
type Handler struct {
	pool         *pgxpool.Pool
	q            *db.Queries
	river        *river.Client[pgx.Tx]
	enc          *crypto.Encryptor
	catalog      map[string]sourcedef.Definition
	maxBodyBytes int64
	limiter      *rateLimiter
}

// Register mounts the ingest endpoint on mux. The catalog is loaded once at
// boot and shared with the sources API so both sides agree on provider slugs.
// It returns the Handler so callers outside this package (the test-event
// generator, #25) can drive the same transactional core through ProcessEvent.
func Register(mux *http.ServeMux, pool *pgxpool.Pool, q *db.Queries, riverClient *river.Client[pgx.Tx], enc *crypto.Encryptor, catalog map[string]sourcedef.Definition, opts Options) *Handler {
	h := &Handler{
		pool:         pool,
		q:            q,
		river:        riverClient,
		enc:          enc,
		catalog:      catalog,
		maxBodyBytes: opts.MaxBodyBytes,
		limiter:      newRateLimiter(opts.RateLimitPerSecond),
	}
	mux.Handle("POST /ingest/{path}", http.HandlerFunc(h.ingest))
	return h
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	source, err := h.q.GetSourceByEndpointPath(ctx, r.PathValue("path"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "unknown source")
			return
		}
		slog.Error("looking up source", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Rate-limit per source once it's resolved: an unknown path already 404'd,
	// so buckets only exist for real sources. Burst over the limit gets 429.
	if !h.limiter.allow(source.EndpointPath) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	body, tooLarge, err := readBody(w, r, h.maxBodyBytes)
	if tooLarge {
		// The cap trips mid-read, before the insert, so nothing is persisted.
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	if err != nil {
		// A body read failure is a client/transport problem, not ours; there is
		// nothing durable to store, so reject rather than persist a partial event.
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	_, verified, err := h.ProcessEvent(ctx, source, body, r.Header)
	if err != nil {
		slog.Error("processing event", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	observability.RecordEventIngested(verified)

	// Ack only after the commit: the event is durable before the provider hears 200.
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// ProcessEvent verifies, stores, rule-evaluates, and fans out one webhook —
// the transactional core shared by the real ingest endpoint above and the
// test-event generator (#25), so a generated test event exercises real
// verification and rule evaluation rather than bypassing them.
func (h *Handler) ProcessEvent(ctx context.Context, source db.Source, body []byte, headers http.Header) (db.InsertEventRow, bool, error) {
	// A failed or errored check does not fail the request: the event is stored
	// either way, with verified reflecting the outcome, so a misconfigured
	// secret surfaces in the event log instead of vanishing (per Phase 1 decision).
	verified := h.verify(source, body, headers)

	// raw_headers is stored as JSONB verbatim; http.Header marshals as
	// map[string][]string, so multi-valued headers survive round-trip.
	rawHeaders, err := json.Marshal(headers)
	if err != nil {
		return db.InsertEventRow{}, false, fmt.Errorf("marshaling headers: %w", err)
	}

	// Best-effort JSON parse: store the body as parsed_body only when it is
	// valid JSON, otherwise leave the column NULL for unstructured payloads.
	var parsedBody []byte
	if json.Valid(body) {
		parsedBody = body
	}

	contentType := headers.Get("Content-Type")

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return db.InsertEventRow{}, false, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	qtx := h.q.WithTx(tx)
	event, err := qtx.InsertEvent(ctx, db.InsertEventParams{
		TenantID:    source.TenantID,
		SourceID:    source.ID,
		RawHeaders:  rawHeaders,
		RawBody:     body,
		ContentType: pgtype.Text{String: contentType, Valid: contentType != ""},
		ParsedBody:  parsedBody,
		DedupeKey:   pgtype.Text{},
		Verified:    verified,
	})
	if err != nil {
		return db.InsertEventRow{}, false, fmt.Errorf("inserting event: %w", err)
	}

	// Rules (BR-14): the first matching enabled rule may drop the event or
	// override the fan-out set. Evaluated inside the event's tx so the
	// decision and its consequences commit atomically. Replays deliberately
	// bypass this — replaying a dropped event is an explicit operator override.
	ruleRows, err := qtx.ListEnabledRulesForSource(ctx, source.ID)
	if err != nil {
		return db.InsertEventRow{}, false, fmt.Errorf("loading rules: %w", err)
	}
	decision := rules.Evaluate(ruleRows, ruleInput(parsedBody, rawHeaders, source))

	switch decision.Action {
	case rules.ActionDrop:
		// Stored but not fanned out; dropped_reason records which rule said so.
		if err := qtx.MarkEventDropped(ctx, db.MarkEventDroppedParams{
			ID:            event.ID,
			DroppedReason: pgtype.Text{String: decision.RuleName, Valid: true},
		}); err != nil {
			return db.InsertEventRow{}, false, fmt.Errorf("marking event dropped: %w", err)
		}
	case rules.ActionRoute:
		rows, err := qtx.ListDeliveryTargetsByDestinationIDs(ctx, db.ListDeliveryTargetsByDestinationIDsParams{
			DestinationIds: decision.RouteDestinationIDs,
			TenantID:       source.TenantID,
		})
		if err != nil {
			return db.InsertEventRow{}, false, fmt.Errorf("loading rule route destinations: %w", err)
		}
		targets := make([]queue.DeliveryTarget, len(rows))
		for i, row := range rows {
			targets[i] = queue.DeliveryTarget{
				DestinationID:      row.DestinationID,
				MaxAttempts:        row.MaxAttempts,
				BackoffBaseSeconds: row.BackoffBaseSeconds,
				BackoffMaxSeconds:  row.BackoffMaxSeconds,
			}
		}
		if _, err := queue.EnqueueDeliveriesTo(ctx, h.river, tx, qtx, source.TenantID, event.ID, targets); err != nil {
			return db.InsertEventRow{}, false, fmt.Errorf("enqueuing rule-routed deliveries: %w", err)
		}
	default:
		// Fan out to every destination this source is routed to
		if _, err := queue.EnqueueDeliveries(ctx, h.river, tx, qtx, source.TenantID, source.ID, event.ID); err != nil {
			return db.InsertEventRow{}, false, fmt.Errorf("enqueuing deliveries: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return db.InsertEventRow{}, false, fmt.Errorf("committing event: %w", err)
	}

	return event, verified, nil
}

// ruleInput shapes the stored event fields for rule evaluation. Unmarshal
// failures leave the field nil — expressions over it then error per-rule and
// fail open, matching the package's "broken rule never blocks ingest" rule.
func ruleInput(parsedBody, rawHeaders []byte, source db.Source) rules.Input {
	var body any
	if parsedBody != nil {
		_ = json.Unmarshal(parsedBody, &body)
	}
	var headers map[string]any
	_ = json.Unmarshal(rawHeaders, &headers)
	return rules.Input{
		Body:    body,
		Headers: headers,
		Source:  map[string]any{"name": source.Name, "provider_type": source.ProviderType},
	}
}

// verify runs the source's provider verifier and reports whether the signature
// checked out. A source with provider_type "none" has no catalog entry and is
// always treated as verified. A decrypt or verifier error is logged and counts as unverified rather
// than dropping the event.
func (h *Handler) verify(source db.Source, body []byte, headers http.Header) bool {
	def, ok := h.catalog[source.ProviderType]
	if !ok {
		// Only "none" reaches ingest without a catalog entry — source creation
		// rejects any other unknown provider_type.
		return true
	}

	var secret []byte
	if len(source.SigningSecretEncrypted) > 0 {
		plaintext, err := h.enc.Decrypt(source.SigningSecretEncrypted)
		if err != nil {
			slog.Error("decrypting signing secret", "error", err, "provider", source.ProviderType)
			return false
		}
		secret = plaintext
	}

	verified, err := sourcedef.Verify(def, body, headers, secret)
	if err != nil {
		slog.Error("verifying webhook", "error", err, "provider", source.ProviderType)
		return false
	}
	return verified
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("encoding JSON response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
