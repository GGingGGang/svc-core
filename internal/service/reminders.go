package service

import (
	"context"

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
func (s *Service) AddReminder(ctx context.Context, userID, scheduleID uuid.UUID, minutesBefore int32, channel string) (*Reminder, error) {
	sch, err := s.GetSchedule(ctx, userID, scheduleID)
	if err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if err := s.q.AddReminder(ctx, repo.AddReminderParams{
		ID:            idBytes(id),
		ScheduleID:    idBytes(scheduleID),
		MinutesBefore: minutesBefore,
		Channel:       repo.ScheduleRemindersChannel(channel),
	}); err != nil {
		return nil, err
	}
	occurredAt := s.occurredAt(ctx)

	rows, err := s.q.ListReminders(ctx, idBytes(scheduleID))
	if err != nil {
		return nil, err
	}
	reminders, err := mapReminders(rows)
	if err != nil {
		return nil, err
	}
	sch.Reminders = reminders
	s.publishUpdated(ctx, sch, occurredAt)

	for _, rem := range reminders {
		if rem.ID == id {
			created := rem
			return &created, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteReminder removes the reminder, then publishes schedules.updated.v1
// with the remaining reminders snapshot — same "updated" event as any other
// schedule mutation (../../PLAN.md §5.2).
func (s *Service) DeleteReminder(ctx context.Context, userID, scheduleID, reminderID uuid.UUID) error {
	sch, err := s.GetSchedule(ctx, userID, scheduleID)
	if err != nil {
		return err
	}

	n, err := s.q.DeleteReminder(ctx, repo.DeleteReminderParams{ID: idBytes(reminderID), ScheduleID: idBytes(scheduleID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	occurredAt := s.occurredAt(ctx)

	rows, err := s.q.ListReminders(ctx, idBytes(scheduleID))
	if err != nil {
		return err
	}
	reminders, err := mapReminders(rows)
	if err != nil {
		return err
	}
	sch.Reminders = reminders
	s.publishUpdated(ctx, sch, occurredAt)
	return nil
}
