package service

import (
	"context"
	"log"
	"time"

	"github.com/GGingGGang/svc-core/internal/events"
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

func (s *Service) publishCreated(ctx context.Context, sch *Schedule, occurredAt time.Time) {
	if s.pub == nil {
		return
	}
	if err := s.pub.PublishScheduleCreated(ctx, toScheduleEvent(sch, occurredAt)); err != nil {
		log.Printf("ERROR publish %s failed: schedule_id=%s err=%v", events.SubjectScheduleCreated, sch.ID, err)
	}
}

func (s *Service) publishUpdated(ctx context.Context, sch *Schedule, occurredAt time.Time) {
	if s.pub == nil {
		return
	}
	if err := s.pub.PublishScheduleUpdated(ctx, toScheduleEvent(sch, occurredAt)); err != nil {
		log.Printf("ERROR publish %s failed: schedule_id=%s err=%v", events.SubjectScheduleUpdated, sch.ID, err)
	}
}

func (s *Service) publishDeleted(ctx context.Context, scheduleID, userID string, occurredAt time.Time) {
	if s.pub == nil {
		return
	}
	evt := events.ScheduleDeletedEvent{ScheduleID: scheduleID, UserID: userID, OccurredAt: occurredAt}
	if err := s.pub.PublishScheduleDeleted(ctx, evt); err != nil {
		log.Printf("ERROR publish %s failed: schedule_id=%s err=%v", events.SubjectScheduleDeleted, scheduleID, err)
	}
}
