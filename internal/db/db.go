// Package db wires up the database/sql connection pool used by internal/repo.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/GGingGGang/svc-core/internal/config"
)

// Connect opens the MySQL connection pool and verifies it is reachable.
func Connect(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	sqlDB, err := sql.Open("mysql", dsn(cfg))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return sqlDB, nil
}

func dsn(cfg config.Config) string {
	tls := "false"
	if cfg.DBTLS {
		tls = "skip-verify"
	}
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=UTC&tls=%s",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName, tls,
	)
}
