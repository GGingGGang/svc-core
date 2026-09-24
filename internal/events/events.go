// Package events publishes the schedules domain events to NATS JetStream
// (../../PLAN.md §7) and declares the stream those events flow through.
package events

import "time"

const (
	// StreamName is the JetStream stream core owns and declares at startup
	// (../../PLAN.md §7.2). Its subjects are listed explicitly rather than as
	// a wildcard: a wildcard would also match the DLQ's app.schedules.dlq.>
	// subjects that batch declares on APP_SCHEDULES_DLQ, and JetStream
	// refuses to create streams with overlapping subject sets.
	StreamName = "APP_SCHEDULES"

	SubjectScheduleCreated = "app.schedules.created.v1"
	SubjectScheduleUpdated = "app.schedules.updated.v1"
	SubjectScheduleDeleted = "app.schedules.deleted.v1"

	streamMaxAge   = 7 * 24 * time.Hour
	streamMaxBytes = 1 << 30 // 1GiB
)

// ReminderSnapshot mirrors one entry of the `reminders` array in
// ../../PLAN.md §7.3 — the full set of reminders on the schedule at the time
// of the event, not an incremental change.
type ReminderSnapshot struct {
	MinutesBefore int32  `json:"minutes_before"`
	Channel       string `json:"channel"`
}

// ScheduleEvent is the payload published on both SubjectScheduleCreated and
// SubjectScheduleUpdated — the two events share one schema (../../PLAN.md
// §7.3).
type ScheduleEvent struct {
	ScheduleID string             `json:"schedule_id"`
	UserID     string             `json:"user_id"`
	Title      string             `json:"title"`
	StartAt    time.Time          `json:"start_at"`
	EndAt      *time.Time         `json:"end_at"`
	AllDay     bool               `json:"all_day"`
	Source     string             `json:"source"`
	Status     string             `json:"status"`
	Revision   int64              `json:"revision"`
	Reminders  []ReminderSnapshot `json:"reminders"`
	OccurredAt time.Time          `json:"occurred_at"`
}

// ScheduleDeletedEvent is the payload published on SubjectScheduleDeleted
// (../../PLAN.md §7.4).
type ScheduleDeletedEvent struct {
	ScheduleID string    `json:"schedule_id"`
	UserID     string    `json:"user_id"`
	OccurredAt time.Time `json:"occurred_at"`
	Revision   int64     `json:"revision"`
}
