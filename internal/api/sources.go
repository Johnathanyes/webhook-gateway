package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/auth"
	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/sourcedef"
	"webhook-gateway/internal/tenancy"
)

// RegisterSources mounts the minimal sources API on mux, guarded by the admin
// password. Phase 1 only needs create and list — full CRUD comes later. The
// catalog is passed in (loaded once at boot, shared with ingest) so create can
// reject a provider_type that has no verifier.
func RegisterSources(mux *http.ServeMux, q *db.Queries, enc *crypto.Encryptor, catalog map[string]sourcedef.Definition, adminPassword string) {
	h := &sourcesHandler{q: q, enc: enc, catalog: catalog}
	mux.Handle("POST /api/sources", auth.AdminOnly(adminPassword, http.HandlerFunc(h.create)))
	mux.Handle("GET /api/sources", auth.AdminOnly(adminPassword, http.HandlerFunc(h.list)))
}

type sourcesHandler struct {
	q       *db.Queries
	enc     *crypto.Encryptor
	catalog map[string]sourcedef.Definition
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

	// Encrypt the signing secret at rest (BR-26). An empty secret is allowed
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

// generateEndpointPath returns an unguessable path segment — "src_" followed
// by 32 hex chars from 16 crypto/rand bytes — that the provider posts webhooks
// to (BR-01).
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
