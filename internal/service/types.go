package service

import (
	"time"

	"github.com/google/uuid"
)

type Schedule struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Title        string
	Description  *string
	Location     *string
	StartAt      time.Time
	EndAt        *time.Time
	AllDay       bool
	Status       string
	Source       string
	ExtractionID *uuid.UUID
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Reminders    []Reminder
}

// ScheduleFields is the fully-resolved set of mutable schedule columns —
// callers (the API layer) merge partial PATCH input against the current
// row before calling UpdateSchedule, so the service always writes a
// complete row rather than reasoning about which fields changed.
type ScheduleFields struct {
	Title       string
	Description *string
	Location    *string
	StartAt     time.Time
	EndAt       *time.Time
	AllDay      bool
	Status      string
}

type CreateScheduleInput struct {
	Title        string
	Description  *string
	Location     *string
	StartAt      time.Time
	EndAt        *time.Time
	AllDay       bool
	Status       string
	Source       string
	ExtractionID *uuid.UUID
	Reminders    []ReminderInput
}

type Reminder struct {
	ID            uuid.UUID
	ScheduleID    uuid.UUID
	MinutesBefore int32
	Channel       string
	CreatedAt     time.Time
}

type ReminderInput struct {
	MinutesBefore int32
	Channel       string
}
