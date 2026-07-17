package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// writeJSON writes body as a JSON response with the given status. A nil body
// writes just the status line (for 204-style replies).
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

// writeError writes a JSON {"error": message} body. Messages are intentionally
// generic so they don't leak internals to the caller.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
