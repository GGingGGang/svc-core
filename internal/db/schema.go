package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrRequiredSchema = errors.New("required database schema is unavailable")

// CheckReady verifies that the process can reach its database and tables
// required by schedule writes. Startup applies migrations before this.
func CheckReady(ctx context.Context, sqlDB *sql.DB) error {
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: database ping: %v", ErrRequiredSchema, err)
	}
	for _, table := range []string{"schedules", "schedule_reminders", "event_outbox", "schedule_create_requests", "schedule_mutation_requests"} {
		rows, err := sqlDB.QueryContext(ctx, "SELECT 1 FROM "+table+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrRequiredSchema, table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrRequiredSchema, table, err)
		}
	}
	return nil
}
