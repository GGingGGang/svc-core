package service

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Schedule struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Title           string
	Description     *string
	Location        *string
	StartAt         time.Time
	EndAt           *time.Time
	AllDay          bool
	Timezone        string
	StartDate       *string
	EndDate         *string
	Status          string
	Source          string
	ExtractionID    *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Revision        int64
	Reminders       []Reminder
	SeriesID        *uuid.UUID
	OccurrenceIndex *int
	Recurrence      *RecurrenceRule
}

// SchedulePatch contains only supplied fields. Pointer-to-pointer fields
// distinguish omission from an explicit null that clears a nullable column.
type SchedulePatch struct {
	ExpectedRevision *int64
	Scope            string
	StartFold        string
	EndFold          string
	Title            *string
	Description      **string
	Location         **string
	StartAt          *time.Time
	EndAt            **time.Time
	AllDay           *bool
	Timezone         *string
	StartDate        **string
	EndDate          **string
	Status           *string
}

type ScheduleFields struct {
	Title       string
	Description *string
	Location    *string
	StartAt     time.Time
	EndAt       *time.Time
	AllDay      bool
	Timezone    string
	StartDate   *string
	EndDate     *string
	Status      string
}

type ScheduleCursor struct {
	StartAt time.Time
	ID      uuid.UUID
}

type CreateScheduleInput struct {
	IdempotencyKey string
	Title          string
	Description    *string
	Location       *string
	StartAt        time.Time
	EndAt          *time.Time
	AllDay         bool
	Timezone       string
	StartDate      *string
	EndDate        *string
	Status         string
	Source         string
	ExtractionID   *uuid.UUID
	Reminders      []ReminderInput
	Recurrence     *RecurrenceRule
}

type RecurrenceRule struct {
	Frequency string   `json:"frequency"`
	Interval  int      `json:"interval"`
	Count     *int     `json:"count,omitempty"`
	Until     *string  `json:"until,omitempty"`
	Weekdays  []string `json:"weekdays,omitempty"`
	Exdates   []string `json:"exdates,omitempty"`
}

type ScheduleOccurrence struct {
	StartAt    time.Time
	EndAt      *time.Time
	AllDay     bool
	StartDate  *string
	EndDate    *string
	LocalStart time.Time
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
