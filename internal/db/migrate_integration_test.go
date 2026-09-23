//go:build integration

package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/GGingGGang/svc-core/internal/config"
)

func TestMigrateUpFromVersionOne(t *testing.T) {
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8.0.36",
		tcmysql.WithDatabase("core"),
		tcmysql.WithUsername("core_test"),
		tcmysql.WithPassword("core_test"),
		tcmysql.WithScripts("../../db/migrations/000001_init.up.sql"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })

	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC", "multiStatements=true")
	require.NoError(t, err)
	sqlDB, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.Eventually(t, func() bool { return sqlDB.PingContext(ctx) == nil }, 30*time.Second, 500*time.Millisecond)
	_, err = sqlDB.ExecContext(ctx, "CREATE TABLE schema_migrations (version BIGINT NOT NULL PRIMARY KEY, dirty BOOLEAN NOT NULL); INSERT INTO schema_migrations (version, dirty) VALUES (1, FALSE)")
	require.NoError(t, err)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	cfg := config.Config{DBHost: host, DBPort: port.Port(), DBUser: "core_test", DBPassword: "core_test", DBName: "core", DBTLS: false}

	require.NoError(t, MigrateUp(ctx, cfg), "v1 deployment must apply all pending migrations")
	require.NoError(t, CheckReady(ctx, sqlDB))
	require.NoError(t, MigrateUp(ctx, cfg), "repeat startup must be idempotent")
	var version int
	var dirty bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, 3, version)
	require.False(t, dirty)

	_, err = sqlDB.ExecContext(ctx, "UPDATE schema_migrations SET dirty=TRUE")
	require.NoError(t, err)
	var dirtyErr migrate.ErrDirty
	require.ErrorAs(t, MigrateUp(ctx, cfg), &dirtyErr, "dirty migration history must stop startup")
	require.Equal(t, 3, dirtyErr.Version)
}

func TestMigrateUpOnNewDatabase(t *testing.T) {
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8.0.36",
		tcmysql.WithDatabase("core"),
		tcmysql.WithUsername("core_test"),
		tcmysql.WithPassword("core_test"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	cfg := config.Config{DBHost: host, DBPort: port.Port(), DBUser: "core_test", DBPassword: "core_test", DBName: "core", DBTLS: false}
	require.NoError(t, MigrateUp(ctx, cfg))
}
