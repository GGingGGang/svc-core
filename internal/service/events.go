package service

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/outbox"
	"github.com/google/uuid"
)

// occurredAt asks the database for its current UTC time, immediately after
// the write it is timestamping has committed (../../PLAN.md §7.3: "occurred_at
// = core DB 커밋 시각"). It never fails the caller's already-successful
// mutation: if the follow-up query itself errors, it logs and falls back to
// the application clock so the event still carries a usable timestamp.
func (s *Service) occurredAt(ctx context.Context) time.Time {
	var t time.Time
	if err := s.db.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(3)").Scan(&t); err != nil {
		log.Printf("ERROR read db commit timestamp failed, falling back to app clock: %v", err)
		return time.Now().UTC()
	}
	return t.UTC()
}

func occurredAtTx(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var t time.Time
	if err := tx.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(3)").Scan(&t); err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// toScheduleEvent builds the schedules.created.v1 / schedules.updated.v1
// payload (../../PLAN.md §7.3) — reminders is always the full snapshot at
// this point in time, never an incremental change.
func toScheduleEvent(sch *Schedule, occurredAt time.Time) events.ScheduleEvent {
	reminders := make([]events.ReminderSnapshot, 0, len(sch.Reminders))
	for _, r := range sch.Reminders {
		reminders = append(reminders, events.ReminderSnapshot{
			MinutesBefore: r.MinutesBefore,
			Channel:       r.Channel,
		})
	}
	return events.ScheduleEvent{
		ScheduleID: sch.ID.String(),
		UserID:     sch.UserID.String(),
		Title:      sch.Title,
		StartAt:    sch.StartAt,
		EndAt:      sch.EndAt,
		AllDay:     sch.AllDay,
		Source:     sch.Source,
		Reminders:  reminders,
		OccurredAt: occurredAt,
	}
}

func enqueueScheduleEvent(ctx context.Context, tx *sql.Tx, subject string, sch *Schedule) error {
	occurredAt, err := occurredAtTx(ctx, tx)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, subject, sch.ID, occurredAt, toScheduleEvent(sch, occurredAt))
}

func enqueueDeletedEvent(ctx context.Context, tx *sql.Tx, scheduleID, userID uuid.UUID, occurredAt time.Time) error {
	evt := events.ScheduleDeletedEvent{ScheduleID: scheduleID.String(), UserID: userID.String(), OccurredAt: occurredAt}
	return outbox.Enqueue(ctx, tx, events.SubjectScheduleDeleted, scheduleID, occurredAt, evt)
}
