package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
)

var ErrRequiredSchema = errors.New("required database schema is unavailable")

// CheckReady verifies the database tables and columns required by schedule
// writes. Startup applies migrations before this.
func CheckReady(ctx context.Context, sqlDB *sql.DB) error {
	if err := sqlDB.PingContext(ctx); err != nil {
		log.Printf("ERROR readiness failed: dependency=database")
		return fmt.Errorf("%w: database ping: %v", ErrRequiredSchema, err)
	}
	for _, check := range []struct{ table, columns string }{
		{"schedules", "id,user_id,title,description,location,start_at,end_at,all_day,status,source,extraction_id,created_at,updated_at,revision"},
		{"schedule_reminders", "id,schedule_id,minutes_before,channel,created_at"},
		{"event_outbox", "id,subject,schedule_id,occurred_at,payload,headers,attempts,available_at,locked_until,locked_by,last_error,published_at,created_at"},
		{"schedule_create_requests", "user_id,idempotency_key,request_hash,response_json,expires_at"},
		{"schedule_mutation_requests", "user_id,operation,idempotency_key,schedule_id,request_hash,response_json,expires_at"},
	} {
		rows, err := sqlDB.QueryContext(ctx, "SELECT "+check.columns+" FROM "+check.table+" LIMIT 0")
		if err != nil {
			log.Printf("ERROR readiness failed: table=%s", check.table)
			return fmt.Errorf("%w: %s: %v", ErrRequiredSchema, check.table, err)
		}
		if err := rows.Close(); err != nil {
			log.Printf("ERROR readiness failed: table=%s", check.table)
			return fmt.Errorf("%w: %s: %v", ErrRequiredSchema, check.table, err)
		}
	}
	return nil
}
