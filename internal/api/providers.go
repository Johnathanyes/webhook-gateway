package api

import (
	"net/http"
	"slices"
	"strings"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/sourcedef"
)

// RegisterProviders exposes the source-definition catalog read-only. The
// dashboard's source form needs the list of provider slugs, and hardcoding it
// there would defeat the point of a catalog you extend by dropping a YAML file
// in internal/sourcedef/catalog/.
func RegisterProviders(mux *http.ServeMux, catalog map[string]sourcedef.Definition, authz *middleware.Auth) {
	h := &providersHandler{catalog: catalog}
	mux.Handle("GET /api/providers", authz.RequireScope(middleware.ScopeRead, http.HandlerFunc(h.list)))
}

type providersHandler struct {
	catalog map[string]sourcedef.Definition
}

type providerResponse struct {
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	VerificationType string `json:"verification_type"`
	// RequiresSecret tells the UI whether to demand a signing secret before
	// letting the source be created — the same rule the create handler enforces.
	RequiresSecret bool `json:"requires_secret"`
}

func (h *providersHandler) list(w http.ResponseWriter, _ *http.Request) {
	providers := make([]providerResponse, 0, len(h.catalog)+1)
	for _, def := range h.catalog {
		providers = append(providers, providerResponse{
			Slug:             def.Slug,
			Name:             def.Name,
			Description:      def.Description,
			VerificationType: def.Verification.Type,
			RequiresSecret:   def.Verification.Type != "none",
		})
	}
	// Map iteration order is random and the UI wants a stable list.
	slices.SortFunc(providers, func(a, b providerResponse) int {
		return strings.Compare(a.Name, b.Name)
	})

	// "none" has no catalog entry — it is the built-in "accept anything,
	// unverified" option — but a caller choosing a provider needs to see it.
	// Last, so real providers lead the list.
	providers = append(providers, providerResponse{
		Slug:             "none",
		Name:             "None",
		Description:      "No signature verification. Events are stored unverified.",
		VerificationType: "none",
	})

	writeJSON(w, http.StatusOK, providers)
}
