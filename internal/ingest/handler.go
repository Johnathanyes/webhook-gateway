// Package ingest receives inbound provider webhooks and durably stores them
// (BR-01/04/05). It is the hot path: a request is looked up, verified, and
// persisted verbatim, then acknowledged — nothing slower runs before the 200.
package ingest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/sourcedef"
)

// Options carries the ingest abuse-prevention knobs (BR-06).
type Options struct {
	MaxBodyBytes       int64 // reject bodies larger than this with 413
	RateLimitPerSecond int   // per-source token-bucket rate; also the burst
}

// Handler stores inbound webhooks. It holds the pool directly (not just
// Queries) because the insert runs in an explicit transaction, so Phase 2 can
// enqueue a River job in the same tx as a small diff.
type Handler struct {
	pool         *pgxpool.Pool
	q            *db.Queries
	enc          *crypto.Encryptor
	catalog      map[string]sourcedef.Definition
	maxBodyBytes int64
	limiter      *rateLimiter
}

// Register mounts the ingest endpoint on mux. The catalog is loaded once at
// boot and shared with the sources API so both sides agree on provider slugs.
func Register(mux *http.ServeMux, pool *pgxpool.Pool, q *db.Queries, enc *crypto.Encryptor, catalog map[string]sourcedef.Definition, opts Options) {
	h := &Handler{
		pool:         pool,
		q:            q,
		enc:          enc,
		catalog:      catalog,
		maxBodyBytes: opts.MaxBodyBytes,
		limiter:      newRateLimiter(opts.RateLimitPerSecond),
	}
	mux.Handle("POST /ingest/{path}", http.HandlerFunc(h.ingest))
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

	// A failed or errored check does not fail the request: the event is stored
	// either way, with verified reflecting the outcome, so a misconfigured
	// secret surfaces in the event log instead of vanishing (per Phase 1 decision).
	verified := h.verify(source, body, r.Header)

	// raw_headers is stored as JSONB verbatim; http.Header marshals as
	// map[string][]string, so multi-valued headers survive round-trip.
	rawHeaders, err := json.Marshal(r.Header)
	if err != nil {
		slog.Error("marshaling headers", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Best-effort JSON parse: store the body as parsed_body only when it is
	// valid JSON, otherwise leave the column NULL for unstructured payloads.
	var parsedBody []byte
	if json.Valid(body) {
		parsedBody = body
	}

	contentType := r.Header.Get("Content-Type")

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("beginning transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := h.q.WithTx(tx).InsertEvent(ctx, db.InsertEventParams{
		TenantID:    source.TenantID,
		SourceID:    source.ID,
		RawHeaders:  rawHeaders,
		RawBody:     body,
		ContentType: pgtype.Text{String: contentType, Valid: contentType != ""},
		ParsedBody:  parsedBody,
		DedupeKey:   pgtype.Text{}, // dedupe is Phase 2
		Verified:    verified,
	}); err != nil {
		slog.Error("inserting event", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("committing event", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Ack only after the commit: the event is durable before the provider hears 200.
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// verify runs the source's provider verifier and reports whether the signature
// checked out. A source with provider_type "none" has no catalog entry and is
// always treated as verified. A decrypt or verifier error (which a validated
// catalog should make impossible) is logged and counts as unverified rather
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
