package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/repo"
	"github.com/google/uuid"
)

func (s *Service) ListReminders(ctx context.Context, userID, scheduleID uuid.UUID) ([]*Reminder, error) {
	if _, err := s.GetSchedule(ctx, userID, scheduleID); err != nil {
		return nil, err
	}

	rows, err := s.q.ListReminders(ctx, idBytes(scheduleID))
	if err != nil {
		return nil, err
	}

	reminders := make([]*Reminder, 0, len(rows))
	for _, row := range rows {
		rem, err := mapReminder(row)
		if err != nil {
			return nil, err
		}
		reminders = append(reminders, rem)
	}
	return reminders, nil
}

// AddReminder inserts the reminder, then publishes schedules.updated.v1 with
// the full post-insert reminders snapshot (../../PLAN.md §7.3 — reminders is
// always the complete set, never a delta) before returning the created row.
func (s *Service) AddReminder(ctx context.Context, userID, scheduleID uuid.UUID, minutesBefore int32, channel, key string, requestHash [32]byte) (*Reminder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if key != "" {
		response, replay, err := claimScheduleMutation(ctx, tx, userID, scheduleID, "reminder-add", key, requestHash)
		if err != nil {
			return nil, err
		}
		if replay {
			var saved Reminder
			if err := json.Unmarshal(response, &saved); err != nil {
				return nil, err
			}
			if _, err := lockScheduleRevision(ctx, tx, userID, scheduleID); err != nil {
				return nil, err
			}
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM schedule_reminders WHERE id = ? AND schedule_id = ?", idBytes(saved.ID), idBytes(scheduleID)).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
				return nil, ErrIdempotencyConflict
			} else if err != nil {
				return nil, err
			}
			return &saved, nil
		}
	}
	if _, err := lockScheduleRevision(ctx, tx, userID, scheduleID); err != nil {
		return nil, err
	}
	q := s.q.WithTx(tx)
	sch, err := getSchedule(ctx, q, userID, scheduleID)
	if err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if err := q.AddReminder(ctx, repo.AddReminderParams{
		ID:            idBytes(id),
		ScheduleID:    idBytes(scheduleID),
		MinutesBefore: minutesBefore,
		Channel:       repo.ScheduleRemindersChannel(channel),
	}); err != nil {
		return nil, err
	}
	rows, err := q.ListReminders(ctx, idBytes(scheduleID))
	if err != nil {
		return nil, err
	}
	reminders, err := mapReminders(rows)
	if err != nil {
		return nil, err
	}
	sch.Reminders = reminders
	var created *Reminder
	for i := range reminders {
		if reminders[i].ID == id {
			created = &reminders[i]
			break
		}
	}
	if created == nil {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schedules SET revision = revision + 1 WHERE id = ? AND user_id = ?", idBytes(scheduleID), idBytes(userID)); err != nil {
		return nil, err
	}
	sch.Revision++
	if err := enqueueScheduleEvent(ctx, tx, events.SubjectScheduleUpdated, sch); err != nil {
		return nil, err
	}
	if key != "" {
		response, err := json.Marshal(created)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_mutation_requests SET response_json = ? WHERE user_id = ? AND operation = 'reminder-add' AND idempotency_key = ?", response, idBytes(userID), key); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatchOutbox(ctx)

	return created, nil
}

// DeleteReminder removes the reminder, then publishes schedules.updated.v1
// with the remaining reminders snapshot — same "updated" event as any other
// schedule mutation (../../PLAN.md §5.2).
func (s *Service) DeleteReminder(ctx context.Context, userID, scheduleID, reminderID uuid.UUID, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if key != "" {
		hash := sha256.Sum256(idBytes(reminderID))
		_, replay, err := claimScheduleMutation(ctx, tx, userID, scheduleID, "reminder-delete", key, hash)
		if err != nil || replay {
			return err
		}
	}
	if _, err := lockScheduleRevision(ctx, tx, userID, scheduleID); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	sch, err := getSchedule(ctx, q, userID, scheduleID)
	if err != nil {
		return err
	}

	n, err := q.DeleteReminder(ctx, repo.DeleteReminderParams{ID: idBytes(reminderID), ScheduleID: idBytes(scheduleID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	rows, err := q.ListReminders(ctx, idBytes(scheduleID))
	if err != nil {
		return err
	}
	reminders, err := mapReminders(rows)
	if err != nil {
		return err
	}
	sch.Reminders = reminders
	if _, err := tx.ExecContext(ctx, "UPDATE schedules SET revision = revision + 1 WHERE id = ? AND user_id = ?", idBytes(scheduleID), idBytes(userID)); err != nil {
		return err
	}
	sch.Revision++
	if err := enqueueScheduleEvent(ctx, tx, events.SubjectScheduleUpdated, sch); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dispatchOutbox(ctx)
	return nil
}
