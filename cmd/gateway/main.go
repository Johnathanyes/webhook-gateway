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

	"webhook-gateway/internal/api"
	"webhook-gateway/internal/config"
	"webhook-gateway/internal/crypto"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/ingest"
	"webhook-gateway/internal/observability"
	"webhook-gateway/internal/queue"
	"webhook-gateway/internal/sourcedef"
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

	// Ingest and the sources API run in the ingest and all roles. They share one
	// catalog (loaded once) and encryptor so the sources API only creates sources
	// ingest has a verifier for, and both open secrets under the same key.
	if cfg.Role == "all" || cfg.Role == "ingest" {
		enc, err := crypto.NewEncryptor(cfg.EncryptionKey)
		if err != nil {
			return fmt.Errorf("building encryptor: %w", err)
		}
		catalog, err := sourcedef.Load()
		if err != nil {
			return fmt.Errorf("loading source catalog: %w", err)
		}
		q := db.New(pool)

		api.RegisterSources(mux, q, enc, catalog, cfg.AdminPassword)
		ingest.Register(mux, pool, q, enc, catalog, ingest.Options{
			MaxBodyBytes:       cfg.IngestMaxBodyBytes,
			RateLimitPerSecond: cfg.IngestRateLimitPerSecond,
		})
		slog.Info("ingest and sources API mounted")
	}

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
