// Command gateway is the single binary that runs the webhook gateway. All
// logical roles (ingest, worker, dashboard) live in this one process and are
// selected by config; a self-hoster runs them together, cloud splits them by
// setting ROLE per deployment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webhook-gateway/internal/config"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/observability"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to YAML config file (optional; env vars override)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	slog.SetDefault(observability.NewLogger(cfg.LogLevel, cfg.LogFormat))
	slog.Info("configuration loaded", "role", cfg.Role, "port", cfg.Port)

	// Cancelled on the first SIGINT/SIGTERM so shutdown is graceful.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	slog.Info("database pool ready")

	// Both migration sets run on boot (BR-30): our schema via goose, River's
	// own tables via its migrator. Order between them is irrelevant since no
	// table foreign-keys into river_job.
	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	if err := queue.Migrate(ctx, pool); err != nil {
		return err
	}
	slog.Info("migrations applied")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}

	// Serve in the background; a listen failure is pushed back to run() so it
	// exits non-zero instead of hanging.
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("shutdown complete")
	return nil
}
