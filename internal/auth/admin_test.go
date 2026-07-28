package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"webhook-gateway/internal/auth"
)

func TestAdminOnly(t *testing.T) {
	const password = "s3cret-admin-password"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := auth.AdminOnly(password, next)

	tests := []struct {
		name       string
		authHeader string
		want       int
	}{
		{"correct password", "Bearer s3cret-admin-password", http.StatusOK},
		{"wrong password", "Bearer nope", http.StatusUnauthorized},
		{"missing header", "", http.StatusUnauthorized},
		{"no bearer prefix", "s3cret-admin-password", http.StatusUnauthorized},
		{"wrong scheme", "Basic s3cret-admin-password", http.StatusUnauthorized},
		{"password as prefix only", "Bearer s3cret", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// An empty password authenticates nobody. Comparing two empty strings in
// constant time succeeds, so without the non-empty guard a bare "Bearer "
// header would pass — on the middleware that protects API-key minting.
func TestAdminOnlyRejectsEverythingWhenPasswordIsEmpty(t *testing.T) {
	handler := auth.AdminOnly("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler ran with no admin password configured")
		w.WriteHeader(http.StatusOK)
	}))

	for _, header := range []string{"Bearer ", "Bearer", "Bearer anything", ""} {
		t.Run("header="+header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/api-keys", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}
