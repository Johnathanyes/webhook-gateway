package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/sourcedef"
	"webhook-gateway/internal/tenancy"
	"webhook-gateway/internal/api/middleware"

)


// eventProcessor drives an event through real ingest path.
type eventProcessor interface {
	ProcessEvent(ctx context.Context, source db.Source, body []byte, headers http.Header) (db.InsertEventRow, bool, error)
}

// RegisterSources mounts the minimal sources API on mux. Reads need the
// `read` scope, mutations `write`; the admin password passes both.
func RegisterSources(mux *http.ServeMux, q *db.Queries, enc *crypto.Encryptor, catalog map[string]sourcedef.Definition, authz *middleware.Auth, ingester eventProcessor) {
	h := &sourcesHandler{q: q, enc: enc, catalog: catalog, ingester: ingester}
	mux.Handle("POST /api/sources", authz.RequireScope(middleware.ScopeWrite, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/sources", authz.RequireScope(middleware.ScopeRead, http.HandlerFunc(h.list)))
	mux.Handle("POST /api/sources/{id}/test-event", authz.RequireScope(middleware.ScopeWrite, http.HandlerFunc(h.testEvent)))
}

type sourcesHandler struct {
	q        *db.Queries
	enc      *crypto.Encryptor
	catalog  map[string]sourcedef.Definition
	ingester eventProcessor
}

type createSourceRequest struct {
	Name          string `json:"name"`
	ProviderType  string `json:"provider_type"`
	SigningSecret string `json:"signing_secret"`
}

// sourceResponse is the API view of a source. It deliberately omits the
// encrypted secret and key version — those never leave the database.
type sourceResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ProviderType string    `json:"provider_type"`
	EndpointPath string    `json:"endpoint_path"`
	CreatedAt    time.Time `json:"created_at"`
}

func (h *sourcesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" || req.ProviderType == "" {
		writeError(w, http.StatusBadRequest, "name and provider_type are required")
		return
	}

	// provider_type must be a known catalog slug, or the built-in "none"
	// (which has no catalog entry). Rejecting here means a source can't be
	// created that ingest has no verifier for.
	def, inCatalog := h.catalog[req.ProviderType]
	if !inCatalog && req.ProviderType != "none" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider_type %q", req.ProviderType))
		return
	}
	// Every scheme except "none" verifies against a secret, so require one up
	// front rather than silently creating a source that always fails to verify.
	if inCatalog && def.Verification.Type != "none" && req.SigningSecret == "" {
		writeError(w, http.StatusBadRequest, "signing_secret is required for this provider_type")
		return
	}

	path, err := generateEndpointPath()
	if err != nil {
		slog.Error("generating endpoint path", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Encrypt the signing secret at rest. An empty secret is allowed
	// (e.g. provider_type "none"): both secret columns stay NULL.
	var encrypted []byte
	var keyVersion pgtype.Int4
	if req.SigningSecret != "" {
		ciphertext, version, err := h.enc.Encrypt([]byte(req.SigningSecret))
		if err != nil {
			slog.Error("encrypting signing secret", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		encrypted = ciphertext
		keyVersion = pgtype.Int4{Int32: int32(version), Valid: true}
	}

	src, err := h.q.InsertSource(r.Context(), db.InsertSourceParams{
		TenantID:                tenancy.DefaultTenantID,
		Name:                    req.Name,
		ProviderType:            req.ProviderType,
		EndpointPath:            path,
		SigningSecretEncrypted:  encrypted,
		SigningSecretKeyVersion: keyVersion,
		VerificationConfig:      []byte("{}"),
	})
	if err != nil {
		slog.Error("inserting source", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, toSourceResponse(src))
}

func (h *sourcesHandler) list(w http.ResponseWriter, r *http.Request) {
	sources, err := h.q.ListSources(r.Context(), tenancy.DefaultTenantID)
	if err != nil {
		slog.Error("listing sources", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]sourceResponse, len(sources))
	for i, s := range sources {
		out[i] = toSourceResponse(s)
	}
	writeJSON(w, http.StatusOK, out)
}

// testEventResponse reports the outcome of a generated test 
type testEventResponse struct {
	EventID  string `json:"event_id"`
	Verified bool   `json:"verified"`
}

// testEvent signs the provider's sample payload with the
// source's real secret and runs it through the actual ingest path
func (h *sourcesHandler) testEvent(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid source id")
		return
	}
	source, err := h.q.GetSource(r.Context(), db.GetSourceParams{ID: id, TenantID: tenancy.DefaultTenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "source not found")
		return
	}
	if err != nil {
		slog.Error("getting source", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	def, ok := h.catalog[source.ProviderType]
	if !ok || def.SamplePayload == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("provider_type %q has no test-event sample configured", source.ProviderType))
		return
	}

	var secret []byte
	if len(source.SigningSecretEncrypted) > 0 {
		secret, err = h.enc.Decrypt(source.SigningSecretEncrypted)
		if err != nil {
			slog.Error("decrypting signing secret", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	body := []byte(def.SamplePayload)
	headers, err := sourcedef.Sign(def, body, secret)
	if err != nil {
		slog.Error("signing test event", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	event, verified, err := h.ingester.ProcessEvent(r.Context(), source, body, headers)
	if err != nil {
		slog.Error("processing test event", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, testEventResponse{
		EventID:  uuidString(event.ID),
		Verified: verified,
	})
}

// generateEndpointPath returns a path segment — "src_" followed
// by 32 hex chars from 16 crypto/rand bytes — that the provider posts webhooks
// to
func generateEndpointPath() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return "src_" + hex.EncodeToString(b), nil
}

func toSourceResponse(s db.Source) sourceResponse {
	return sourceResponse{
		ID:           uuidString(s.ID),
		Name:         s.Name,
		ProviderType: s.ProviderType,
		EndpointPath: s.EndpointPath,
		CreatedAt:    s.CreatedAt.Time,
	}
}

// uuidString renders a pgtype.UUID in canonical 8-4-4-4-12 form.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
