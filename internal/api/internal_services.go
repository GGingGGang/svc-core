package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GGingGGang/svc-core/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type reminderValidityResponse struct {
	Valid      bool       `json:"valid"`
	ScheduleID string     `json:"schedule_id"`
	UserID     string     `json:"user_id"`
	Title      string     `json:"title"`
	Revision   int64      `json:"revision"`
	Status     string     `json:"status"`
	StartAt    *time.Time `json:"start_at"`
	EndAt      *time.Time `json:"end_at"`
	AllDay     bool       `json:"all_day"`
	Timezone   string     `json:"timezone"`
	StartDate  *string    `json:"start_date"`
	EndDate    *string    `json:"end_date"`
}

func (h *Handler) reminderValidity(w http.ResponseWriter, r *http.Request) {
	if h.internalToken == "" {
		writeError(w, http.StatusServiceUnavailable, "internal service authentication unavailable")
		return
	}
	if !validInternalBearer(r, h.internalToken) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scheduleID, err := uuid.Parse(chi.URLParam(r, "scheduleID"))
	userID, userErr := uuid.Parse(r.URL.Query().Get("user_id"))
	revision, revErr := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	minutesBefore, minutesErr := strconv.ParseInt(r.URL.Query().Get("minutes_before"), 10, 32)
	channel := r.URL.Query().Get("channel")
	requestedRemindAt, remindErr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("remind_at"))
	if err != nil || userErr != nil || revErr != nil || revision <= 0 || minutesErr != nil || minutesBefore < 0 || minutesBefore > 10080 || channel == "" || remindErr != nil {
		writeError(w, http.StatusBadRequest, "invalid reminder validity query")
		return
	}
	response := reminderValidityResponse{ScheduleID: scheduleID.String(), UserID: userID.String()}
	schedule, err := h.svc.GetSchedule(r.Context(), userID, scheduleID)
	if err == nil {
		response.Revision, response.Status, response.Title = schedule.Revision, schedule.Status, schedule.Title
		response.StartAt, response.EndAt = &schedule.StartAt, schedule.EndAt
		response.AllDay, response.Timezone = schedule.AllDay, schedule.Timezone
		response.StartDate, response.EndDate = schedule.StartDate, schedule.EndDate
		matchedReminder := false
		for _, reminder := range schedule.Reminders {
			if reminder.MinutesBefore == int32(minutesBefore) && reminder.Channel == channel {
				matchedReminder = true
				break
			}
		}
		computedRemindAt, computeErr := reminderInstant(schedule, time.Duration(minutesBefore)*time.Minute)
		if channel == "telegram" && schedule.Status == "confirmed" && matchedReminder && computeErr == nil && computedRemindAt.Equal(requestedRemindAt) {
			active, err := h.accountActive(r.Context(), userID)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "account status validation unavailable")
				return
			}
			response.Valid = active
		}
	} else if !errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusServiceUnavailable, "schedule validity lookup unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func reminderInstant(schedule *service.Schedule, offset time.Duration) (time.Time, error) {
	start := schedule.StartAt
	if schedule.AllDay {
		zone, err := time.LoadLocation(schedule.Timezone)
		if err != nil || schedule.StartDate == nil {
			return time.Time{}, errors.New("all-day schedule has no valid local date or timezone")
		}
		date, err := time.Parse("2006-01-02", *schedule.StartDate)
		if err != nil {
			return time.Time{}, err
		}
		start = time.Date(date.Year(), date.Month(), date.Day(), 9, 0, 0, 0, zone)
	}
	return start.Add(-offset), nil
}

func (h *Handler) accountActive(ctx context.Context, userID uuid.UUID) (bool, error) {
	if h.authAccountStatusURL == "" || h.internalToken == "" {
		return false, errors.New("auth status endpoint unavailable")
	}
	endpoint := strings.TrimRight(h.authAccountStatusURL, "/") + "/" + url.PathEscape(userID.String()) + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+h.internalToken)
	response, err := h.internalHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, errors.New("auth status endpoint returned non-200")
	}
	var result struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Active, nil
}
