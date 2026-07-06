package service

import (
	"bytes"
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

func (s *Service) AddReminder(ctx context.Context, userID, scheduleID uuid.UUID, minutesBefore int32, channel string) (*Reminder, error) {
	if _, err := s.GetSchedule(ctx, userID, scheduleID); err != nil {
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

	rows, err := s.q.ListReminders(ctx, idBytes(scheduleID))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if bytes.Equal(row.ID, idBytes(id)) {
			return mapReminder(row)
		}
	}
	return nil, ErrNotFound
}

func (s *Service) DeleteReminder(ctx context.Context, userID, scheduleID, reminderID uuid.UUID) error {
	if _, err := s.GetSchedule(ctx, userID, scheduleID); err != nil {
		return err
	}

	n, err := s.q.DeleteReminder(ctx, repo.DeleteReminderParams{ID: idBytes(reminderID), ScheduleID: idBytes(scheduleID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
