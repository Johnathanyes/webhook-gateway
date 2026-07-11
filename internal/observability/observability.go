// Package observability centralizes how the process logs (and, later, emits
// metrics and traces). Everything goes through slog so switching format or
// sink is a one-line change here instead of a codebase-wide edit — the
// retrofit tax the architecture doc warns about.
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds the process-wide structured logger. level is one of
// debug|info|warn|error (default info); format is json|text (default json).
// Unrecognized values fall back to the defaults rather than erroring — a
// mistyped log level shouldn't stop the process from booting.
func NewLogger(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
