package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/sourcedef"
)

func TestProvidersListsCatalogPlusNone(t *testing.T) {
	catalog := map[string]sourcedef.Definition{
		"stripe": {Slug: "stripe", Name: "Stripe", Verification: sourcedef.Verification{Type: "hmac"}},
		"acme":   {Slug: "acme", Name: "Acme", Verification: sourcedef.Verification{Type: "api_key"}},
	}
	authz := middleware.NewAuth(nil, "test-admin-password")
	mux := http.NewServeMux()
	RegisterProviders(mux, catalog, authz)

	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	req.Header.Set("Authorization", "Bearer test-admin-password")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var got []providerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// Sorted by name, with the built-in "none" last.
	want := []string{"acme", "stripe", "none"}
	if len(got) != len(want) {
		t.Fatalf("got %d providers, want %d: %+v", len(got), len(want), got)
	}
	for i, slug := range want {
		if got[i].Slug != slug {
			t.Errorf("provider %d = %q, want %q", i, got[i].Slug, slug)
		}
	}

	// The UI keys the "signing secret required" field off this.
	if !got[1].RequiresSecret {
		t.Error("stripe.requires_secret = false, want true")
	}
	if got[2].RequiresSecret {
		t.Error("none.requires_secret = true, want false")
	}
}
