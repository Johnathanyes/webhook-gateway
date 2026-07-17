package db

import (
	"context"
	"database/sql"
	"fmt"

	
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"webhook-gateway/migrations"
)

// Migrate applies all pending goose migrations embedded in the migrations
// package, so the binary can migrate itself on boot with no external
// migration tool and no migrations/ directory on disk (BR-30).
//
// Goose drives migrations over database/sql, while the rest of the app uses
// pgx's native pool, so this opens its own short-lived connection rather
// than reusing a *pgxpool.Pool.
func Migrate(ctx context.Context, databaseURL string) (err error) {
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("opening migration connection: %w", err)
	}
	
	defer func() {
		if cerr := sqlDB.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing migration connection: %w", cerr)
		}
	}()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}
