package api

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GGingGGang/svc-core/internal/service"
)

// validStatuses backs the manual status check in the PATCH handler, which
// (unlike create) decodes a raw JSON map rather than a validator-tagged
// struct so it can distinguish "field omitted" from "field set to null".
var validStatuses = map[string]bool{"confirmed": true, "tentative": true, "cancelled": true}

func normalizeTitle(title string) (string, bool) {
	title = strings.TrimSpace(title)
	return title, title != "" && utf8.RuneCountInString(title) <= 255
}

func scheduleInputError(description, location *string, start time.Time, end *time.Time) string {
	if description != nil && utf8.RuneCountInString(*description) > 10000 {
		return "description must contain at most 10000 characters"
	}
	if location != nil && utf8.RuneCountInString(*location) > 255 {
		return "location must contain at most 255 characters"
	}
	// DATETIME(3) stores milliseconds; a smaller gap can become equal on write.
	if end != nil && end.Sub(start) < time.Millisecond {
		return "end_at must be at least 1ms after start_at"
	}
	return ""
}

type reminderRequest struct {
	// no "required" here: 0 (remind exactly at start_at) is a valid value
	// and validator's required treats the zero value as absent.
	MinutesBefore int32  `json:"minutes_before" validate:"gte=0"`
	Channel       string `json:"channel" validate:"required,oneof=push email none"`
}

type reminderResponse struct {
	ID            string    `json:"id"`
	ScheduleID    string    `json:"schedule_id"`
	MinutesBefore int32     `json:"minutes_before"`
	Channel       string    `json:"channel"`
	CreatedAt     time.Time `json:"created_at"`
}

func toReminderResponse(r *service.Reminder) reminderResponse {
	return reminderResponse{
		ID:            r.ID.String(),
		ScheduleID:    r.ScheduleID.String(),
		MinutesBefore: r.MinutesBefore,
		Channel:       r.Channel,
		CreatedAt:     r.CreatedAt,
	}
}

type createScheduleRequest struct {
	Title        string            `json:"title" validate:"required,max=255"`
	Description  *string           `json:"description,omitempty"`
	Location     *string           `json:"location,omitempty" validate:"omitempty,max=255"`
	StartAt      time.Time         `json:"start_at" validate:"required"`
	EndAt        *time.Time        `json:"end_at,omitempty"`
	AllDay       bool              `json:"all_day"`
	Status       string            `json:"status,omitempty" validate:"omitempty,oneof=confirmed tentative cancelled"`
	Source       string            `json:"source,omitempty" validate:"omitempty,oneof=manual ai"`
	ExtractionID *string           `json:"extraction_id,omitempty" validate:"omitempty,uuid"`
	Reminders    []reminderRequest `json:"reminders,omitempty" validate:"omitempty,dive"`
}

type bulkDeleteRequest struct {
	IDs []string `json:"ids" validate:"required,min=1,max=100,dive,uuid"`
}

type scheduleResponse struct {
	ID           string             `json:"id"`
	UserID       string             `json:"user_id"`
	Title        string             `json:"title"`
	Description  *string            `json:"description"`
	Location     *string            `json:"location"`
	StartAt      time.Time          `json:"start_at"`
	EndAt        *time.Time         `json:"end_at"`
	AllDay       bool               `json:"all_day"`
	Status       string             `json:"status"`
	Source       string             `json:"source"`
	ExtractionID *string            `json:"extraction_id,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	Reminders    []reminderResponse `json:"reminders,omitempty"`
}

func toScheduleResponse(s *service.Schedule) scheduleResponse {
	resp := scheduleResponse{
		ID:          s.ID.String(),
		UserID:      s.UserID.String(),
		Title:       s.Title,
		Description: s.Description,
		Location:    s.Location,
		StartAt:     s.StartAt,
		EndAt:       s.EndAt,
		AllDay:      s.AllDay,
		Status:      s.Status,
		Source:      s.Source,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
	if s.ExtractionID != nil {
		id := s.ExtractionID.String()
		resp.ExtractionID = &id
	}
	if len(s.Reminders) > 0 {
		resp.Reminders = make([]reminderResponse, 0, len(s.Reminders))
		for _, r := range s.Reminders {
			rem := r
			resp.Reminders = append(resp.Reminders, toReminderResponse(&rem))
		}
	}
	return resp
}

type extractRequest struct {
	Text     string    `json:"text" validate:"required"`
	Now      time.Time `json:"now" validate:"required"`
	Timezone string    `json:"timezone" validate:"required"`
}

type extractCandidateResponse struct {
	Title             string     `json:"title"`
	StartAt           *time.Time `json:"start_at"`
	EndAt             *time.Time `json:"end_at"`
	AllDay            bool       `json:"all_day"`
	Location          *string    `json:"location"`
	Description       string     `json:"description"`
	Confidence        float64    `json:"confidence"`
	NeedsConfirmation bool       `json:"needs_confirmation"`
	Issues            []string   `json:"issues"`
}

type extractResponse struct {
	Candidates []extractCandidateResponse `json:"candidates"`
	Truncated  bool                       `json:"truncated"`
}

func toExtractResponse(candidates []service.ExtractCandidate, truncated bool) extractResponse {
	resp := extractResponse{Candidates: make([]extractCandidateResponse, 0, len(candidates)), Truncated: truncated}
	for _, c := range candidates {
		resp.Candidates = append(resp.Candidates, extractCandidateResponse{
			Title:             c.Title,
			StartAt:           c.StartAt,
			EndAt:             c.EndAt,
			AllDay:            c.AllDay,
			Location:          c.Location,
			Description:       c.Description,
			Confidence:        c.Confidence,
			NeedsConfirmation: c.NeedsConfirmation,
			Issues:            c.Issues,
		})
	}
	return resp
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}
