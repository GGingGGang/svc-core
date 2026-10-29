package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type cloneScheduleRequest struct {
	Title        *string         `json:"title"`
	Description  json.RawMessage `json:"description"`
	Location     json.RawMessage `json:"location"`
	StartAt      *time.Time      `json:"start_at"`
	EndAt        json.RawMessage `json:"end_at"`
	AllDay       *bool           `json:"all_day"`
	Timezone     *string         `json:"timezone"`
	StartDate    *string         `json:"start_date"`
	EndDate      *string         `json:"end_date"`
	LocalStartAt *string         `json:"local_start_at"`
	LocalEndAt   *string         `json:"local_end_at"`
	StartFold    string          `json:"start_fold"`
	EndFold      string          `json:"end_fold"`
}

func (h *Handler) CloneSchedule(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sourceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}
	source, err := h.svc.GetSchedule(r.Context(), userID, sourceID)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load source schedule")
		return
	}
	var req cloneScheduleRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
	}
	title := source.Title
	if req.Title != nil {
		var valid bool
		title, valid = normalizeTitle(*req.Title)
		if !valid {
			writeError(w, http.StatusBadRequest, "title must contain 1 to 255 characters")
			return
		}
	}
	description, location := source.Description, source.Location
	if len(req.Description) > 0 {
		if err := json.Unmarshal(req.Description, &description); err != nil {
			writeError(w, http.StatusBadRequest, "invalid description")
			return
		}
	}
	if len(req.Location) > 0 {
		if err := json.Unmarshal(req.Location, &location); err != nil {
			writeError(w, http.StatusBadRequest, "invalid location")
			return
		}
	}
	if message := scheduleTextInputError(description, location); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	timezone := source.Timezone
	if req.Timezone != nil {
		timezone = *req.Timezone
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
		return
	}
	allDay := source.AllDay
	if req.AllDay != nil {
		allDay = *req.AllDay
	}
	startAt, endAt := source.StartAt, source.EndAt
	if req.StartAt != nil {
		startAt = req.StartAt.UTC()
	}
	if len(req.EndAt) > 0 {
		if err := json.Unmarshal(req.EndAt, &endAt); err != nil {
			writeError(w, http.StatusBadRequest, "invalid end_at")
			return
		}
		if endAt != nil {
			end := endAt.UTC()
			endAt = &end
		}
	}
	if req.LocalStartAt != nil {
		if req.StartAt != nil {
			writeError(w, http.StatusBadRequest, "provide only one of start_at and local_start_at")
			return
		}
		startAt, err = resolveLocalDateTime(*req.LocalStartAt, timezone, req.StartFold)
		if err != nil {
			writeTimeInputError(w, err)
			return
		}
	}
	if req.LocalEndAt != nil {
		if len(req.EndAt) > 0 {
			writeError(w, http.StatusBadRequest, "provide only one of end_at and local_end_at")
			return
		}
		end, err := resolveLocalDateTime(*req.LocalEndAt, timezone, req.EndFold)
		if err != nil {
			writeTimeInputError(w, err)
			return
		}
		endAt = &end
	}
	startDate, endDate := source.StartDate, source.EndDate
	if req.StartDate != nil || req.EndDate != nil {
		if req.StartDate == nil || req.EndDate == nil {
			writeError(w, http.StatusBadRequest, "start_date and end_date must be supplied together")
			return
		}
		startDate, endDate = req.StartDate, req.EndDate
		allDay = true
	}
	if allDay && (req.StartAt != nil || req.LocalStartAt != nil) && req.StartDate == nil {
		zone, _ := time.LoadLocation(timezone)
		localStart := startAt.In(zone)
		newStartDate := localStart.Format("2006-01-02")
		duration := 1
		if source.StartDate != nil && source.EndDate != nil {
			oldStart, startErr := time.Parse("2006-01-02", *source.StartDate)
			oldEnd, endErr := time.Parse("2006-01-02", *source.EndDate)
			if startErr == nil && endErr == nil && oldEnd.After(oldStart) {
				duration = int(oldEnd.Sub(oldStart).Hours() / 24)
				if duration < 1 {
					duration = 1
				}
			}
		}
		endDateValue := localStart.AddDate(0, 0, duration).Format("2006-01-02")
		startDate, endDate = &newStartDate, &endDateValue
	}
	if allDay && startDate == nil {
		zone, _ := time.LoadLocation(timezone)
		localStart := startAt.In(zone)
		newStartDate := localStart.Format("2006-01-02")
		duration := 1
		if endAt != nil && endAt.After(startAt) {
			days := int(endAt.Sub(startAt).Hours() / 24)
			if endAt.Sub(startAt)%(24*time.Hour) != 0 {
				days++
			}
			if days > duration {
				duration = days
			}
		}
		newEndDate := localStart.AddDate(0, 0, duration).Format("2006-01-02")
		startDate, endDate = &newStartDate, &newEndDate
	}
	if !allDay {
		startDate, endDate = nil, nil
	}
	if allDay && startDate != nil && endDate != nil {
		startAt, endAt, startDate, endDate, err = resolveScheduleTimes(nil, nil, nil, nil, startDate, endDate, true, timezone, "", "")
		if err != nil {
			writeTimeInputError(w, err)
			return
		}
	}
	if message := scheduleInputError(description, location, startAt, endAt); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	key, validKey := idempotencyKey(r)
	if !validKey {
		writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	}
	reminderInputs := make([]service.ReminderInput, 0, len(source.Reminders))
	reminderRequests := make([]reminderRequest, 0, len(source.Reminders))
	for _, reminder := range source.Reminders {
		reminderInputs = append(reminderInputs, service.ReminderInput{MinutesBefore: reminder.MinutesBefore, Channel: reminder.Channel})
		reminderRequests = append(reminderRequests, reminderRequest{MinutesBefore: reminder.MinutesBefore, Channel: reminder.Channel})
	}
	if !h.checkReminderChannel(w, r, userID, reminderRequests) {
		return
	}
	created, err := h.svc.CreateSchedule(r.Context(), userID, service.CreateScheduleInput{
		IdempotencyKey: key, Title: title, Description: description, Location: location,
		StartAt: startAt, EndAt: endAt, AllDay: allDay, Timezone: timezone,
		StartDate: startDate, EndDate: endDate, Status: "confirmed", Source: "manual", Reminders: reminderInputs,
	})
	if errors.Is(err, service.ErrAccountDeleted) {
		writeError(w, http.StatusGone, "account has been deleted")
		return
	}
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if errors.Is(err, service.ErrDuplicateReminder) || errors.Is(err, service.ErrReminderLimit) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clone schedule")
		return
	}
	writeScheduleJSON(w, http.StatusCreated, created)
}
