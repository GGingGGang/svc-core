package service

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

// CleanupDeletedAccount permanently blocks future writes and removes data
// owned by the account in one transaction. The tombstone contains only the
// opaque user UUID and remains after cleanup for delayed-event protection.
func (s *Service) CleanupDeletedAccount(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO account_deletion_tombstones (user_id) VALUES (?)", idBytes(userID)); err != nil {
		return err
	}
	user := userID.String()
	if _, err := tx.ExecContext(ctx, `DELETE FROM event_outbox
WHERE JSON_UNQUOTE(JSON_EXTRACT(payload, '$.user_id')) = ?
   OR schedule_id IN (SELECT id FROM schedules WHERE user_id = ?)`, user, idBytes(userID)); err != nil {
		return err
	}
	for _, query := range []string{
		"DELETE FROM schedule_create_requests WHERE user_id = ?",
		"DELETE FROM schedule_mutation_requests WHERE user_id = ?",
		"DELETE FROM ai_request_admissions WHERE user_id = ?",
		"DELETE FROM ai_request_users WHERE user_id = ?",
		"DELETE FROM ai_extractions WHERE user_id = ?",
		"DELETE FROM schedules WHERE user_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, query, idBytes(userID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureAccountNotDeleted(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error {
	var found []byte
	err := tx.QueryRowContext(ctx, "SELECT user_id FROM account_deletion_tombstones WHERE user_id = ? FOR UPDATE", idBytes(userID)).Scan(&found)
	if err == nil {
		return ErrAccountDeleted
	}
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}
