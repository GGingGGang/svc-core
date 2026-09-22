package db

import (
	"context"
	"errors"
	"fmt"

	migrations "github.com/GGingGGang/svc-core/db"
	"github.com/GGingGGang/svc-core/internal/config"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// MigrateUp applies the embedded, versioned SQL before the server starts.
func MigrateUp(ctx context.Context, cfg config.Config) error {
	sqlDB, err := connect(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	source, err := iofs.New(migrations.Files, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	driver, err := mysql.WithInstance(sqlDB, &mysql.Config{})
	if err != nil {
		return fmt.Errorf("configure migration database: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "mysql", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return CheckReady(ctx, sqlDB)
}
