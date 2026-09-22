package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrRequiredSchema = errors.New("required database schema is unavailable")

// CheckReady verifies that the process can reach its database and the outbox
// table required by the dispatcher. Startup applies migrations before this.
func CheckReady(ctx context.Context, sqlDB *sql.DB) error {
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: database ping: %v", ErrRequiredSchema, err)
	}
	rows, err := sqlDB.QueryContext(ctx, "SELECT 1 FROM event_outbox LIMIT 0")
	if err != nil {
		return fmt.Errorf("%w: event_outbox: %v", ErrRequiredSchema, err)
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: event_outbox: %v", ErrRequiredSchema, err)
	}
	return nil
}
