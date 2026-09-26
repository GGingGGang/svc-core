package service

import (
	"errors"
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
	Revision     int64
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
	IdempotencyKey string
	Title          string
	Description    *string
	Location       *string
	StartAt        time.Time
	EndAt          *time.Time
	AllDay         bool
	Status         string
	Source         string
	ExtractionID   *uuid.UUID
	Reminders      []ReminderInput
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

// ExtractInput is the /schedules/extract request, service-layer shaped
// (../../PLAN.md §5.2/§6). APIKey is the raw X-Gemini-Key header value —
// empty means "use the server's GEMINI_API_KEY fallback".
type ExtractInput struct {
	Text     string
	Now      time.Time
	Timezone string
	APIKey   string
}

// ExtractCandidate mirrors ai.Candidate — kept as its own type so the api
// package only ever imports service, never internal/ai directly (matching
// the existing service-is-the-facade layering).
type ExtractCandidate struct {
	Title             string
	StartAt           *time.Time
	EndAt             *time.Time
	AllDay            bool
	Location          *string
	Description       string
	Confidence        float64
	NeedsConfirmation bool
	Issues            []string
}

// ExtractRateLimitedError is returned by ExtractSchedules when the Gemini
// upstream is still rate limited after one retry (../../PLAN.md §6) — the
// api layer maps it to 429 + Retry-After.
type ExtractRateLimitedError struct {
	RetryAfter time.Duration
}

var ErrExtractBusy = errors.New("extract: another request is in progress")
var ErrExtractUserRateLimited = errors.New("extract: user request limit reached")
var ErrExtractInvalidKey = errors.New("extract: invalid api key")
var ErrExtractModelUnavailable = errors.New("extract: model unavailable")
var ErrExtractUpstreamUnavailable = errors.New("extract: upstream unavailable")

func (e *ExtractRateLimitedError) Error() string {
	return "extract: gemini rate limited"
}
