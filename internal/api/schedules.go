package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

func (h *Handler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	var validTitle bool
	req.Title, validTitle = normalizeTitle(req.Title)
	if !validTitle {
		writeError(w, http.StatusBadRequest, "title must contain 1 to 255 characters")
		return
	}
	if req.Status == "" {
		req.Status = "confirmed"
	}
	if req.Source == "" {
		req.Source = "manual"
	}
	if err := h.val.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	timezone := req.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
		return
	}
	startAt, endAt, startDate, endDate, err := resolveScheduleTimes(req.StartAt, req.EndAt, req.LocalStartAt, req.LocalEndAt, req.StartDate, req.EndDate, req.AllDay, timezone, req.StartFold, req.EndFold)
	if err != nil {
		writeTimeInputError(w, err)
		return
	}
	if message := scheduleInputError(req.Description, req.Location, startAt, endAt); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	key, validKey := idempotencyKey(r)
	if !validKey {
		writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	}

	var extractionID *uuid.UUID
	if req.ExtractionID != nil {
		id, err := uuid.Parse(*req.ExtractionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid extraction_id")
			return
		}
		extractionID = &id
	}

	reminders := make([]service.ReminderInput, 0, len(req.Reminders))
	for _, rem := range req.Reminders {
		reminders = append(reminders, service.ReminderInput{MinutesBefore: rem.MinutesBefore, Channel: rem.Channel})
	}
	if !h.checkReminderChannel(w, r, userID, req.Reminders) {
		return
	}

	sch, err := h.svc.CreateSchedule(r.Context(), userID, service.CreateScheduleInput{
		IdempotencyKey: key,
		Title:          req.Title,
		Description:    req.Description,
		Location:       req.Location,
		StartAt:        startAt,
		EndAt:          endAt,
		AllDay:         req.AllDay,
		Timezone:       timezone,
		StartDate:      startDate,
		EndDate:        endDate,
		Status:         req.Status,
		Source:         req.Source,
		ExtractionID:   extractionID,
		Reminders:      reminders,
	})
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if errors.Is(err, service.ErrAccountDeleted) {
		writeError(w, http.StatusGone, "account has been deleted")
		return
	}
	if errors.Is(err, service.ErrReminderLimit) || errors.Is(err, service.ErrDuplicateReminder) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, service.ErrInvalidReminder) {
		writeError(w, http.StatusBadRequest, "minutes_before must be between 0 and 10080")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create schedule")
		return
	}
	writeScheduleJSON(w, http.StatusCreated, sch)
}

func (h *Handler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}

	sch, err := h.svc.GetSchedule(r.Context(), userID, id)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get schedule")
		return
	}
	writeScheduleJSON(w, http.StatusOK, sch)
}

func (h *Handler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	query := r.URL.Query()
	fromValue, toValue := query.Get("from"), query.Get("to")
	if fromValue == "" || toValue == "" {
		writeError(w, http.StatusBadRequest, "from and to are required")
		return
	}
	from, err := time.Parse(time.RFC3339, fromValue)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid from (want RFC3339)")
		return
	}
	to, err := time.Parse(time.RFC3339, toValue)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid to (want RFC3339)")
		return
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) {
		writeError(w, http.StatusBadRequest, "from must be before to")
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "date range must not exceed 366 days")
		return
	}
	displayTimezone := query.Get("timezone")
	if displayTimezone == "" {
		displayTimezone = "UTC"
	}
	zone, err := time.LoadLocation(displayTimezone)
	if err != nil {
		writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
		return
	}

	var status *string
	if v := query.Get("status"); v != "" {
		if !validStatuses[v] {
			writeError(w, http.StatusBadRequest, "invalid status")
			return
		}
		status = &v
	}

	limitValues, hasLimit := query["limit"]
	cursorValues, hasCursor := query["cursor"]
	search := query.Get("q")
	if hasLimit && len(limitValues) != 1 || hasCursor && len(cursorValues) != 1 {
		writeError(w, http.StatusBadRequest, "limit and cursor may be specified once")
		return
	}
	limit := 20
	if hasLimit {
		limit, err = strconv.Atoi(limitValues[0])
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
	}
	fromDate := from.In(zone).Format("2006-01-02")
	toLocal := to.In(zone)
	toDate := toLocal.Format("2006-01-02")
	if hour, minute, second := toLocal.Clock(); hour != 0 || minute != 0 || second != 0 || toLocal.Nanosecond() != 0 {
		toDate = toLocal.AddDate(0, 0, 1).Format("2006-01-02")
	}
	filterHash := listFilterHash(userID.String(), from, to, status, search, displayTimezone)
	var cursor *service.ScheduleCursor
	if hasCursor {
		token := query.Get("cursor")
		decoded, err := decodeScheduleCursor(token, filterHash)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		if !decoded.StartAt.Before(to) {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursor = decoded
	}
	schedules, hasMore, err := h.svc.ListSchedulesPage(r.Context(), userID, from, to, fromDate, toDate, status, search, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list schedules")
		return
	}
	resp := make([]scheduleResponse, 0, len(schedules))
	for _, schedule := range schedules {
		resp = append(resp, toScheduleResponse(schedule))
	}
	var nextCursor *string
	if hasMore && len(schedules) != 0 {
		last := schedules[len(schedules)-1]
		token := encodeScheduleCursor(last.StartAt, last.ID, filterHash)
		nextCursor = &token
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": resp, "next_cursor": nextCursor})
}

type scheduleCursorToken struct {
	StartAt    time.Time `json:"start_at"`
	ID         string    `json:"id"`
	FilterHash string    `json:"filter_hash"`
}

func listFilterHash(userID string, from, to time.Time, status *string, query, timezone string) string {
	statusValue := ""
	if status != nil {
		statusValue = *status
	}
	filter, _ := json.Marshal([]string{userID, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano), statusValue, query, timezone})
	hash := sha256.Sum256(filter)
	return fmt.Sprintf("%x", hash[:])
}

func encodeScheduleCursor(startAt time.Time, id uuid.UUID, filterHash string) string {
	value, _ := json.Marshal(scheduleCursorToken{StartAt: startAt.UTC(), ID: id.String(), FilterHash: filterHash})
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeScheduleCursor(value, filterHash string) (*service.ScheduleCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var token scheduleCursorToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	if token.FilterHash != filterHash || token.StartAt.IsZero() {
		return nil, errors.New("cursor filter mismatch")
	}
	id, err := uuid.Parse(token.ID)
	if err != nil {
		return nil, err
	}
	return &service.ScheduleCursor{StartAt: token.StartAt.UTC(), ID: id}, nil
}

func (h *Handler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}
	expectedRevision, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		writeError(w, http.StatusPreconditionRequired, "If-Match with schedule ETag is required")
		return
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	key, validKey := idempotencyKey(r)
	if !validKey {
		writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	}
	canonical, err := json.Marshal(struct {
		Body any    `json:"body"`
		ETag string `json:"if_match"`
	}{Body: raw, ETag: r.Header.Get("If-Match")})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	requestHash := sha256.Sum256(canonical)
	replay, found, err := h.svc.ReplayScheduleUpdate(r.Context(), userID, id, key, requestHash)
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load schedule retry")
		return
	}
	if found {
		writeScheduleJSON(w, http.StatusOK, replay)
		return
	}

	fields := service.SchedulePatch{}
	fields.ExpectedRevision = &expectedRevision

	if v, ok := raw["title"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil || p == nil {
			writeError(w, http.StatusBadRequest, "title must be a non-empty string")
			return
		}
		title, valid := normalizeTitle(*p)
		if !valid {
			writeError(w, http.StatusBadRequest, "title must contain 1 to 255 characters")
			return
		}
		fields.Title = &title
	}
	if v, ok := raw["description"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid description")
			return
		}
		fields.Description = &p
	}
	if v, ok := raw["location"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid location")
			return
		}
		fields.Location = &p
	}
	if v, ok := raw["start_at"]; ok {
		var p *time.Time
		if err := json.Unmarshal(v, &p); err != nil || p == nil {
			writeError(w, http.StatusBadRequest, "start_at must be a non-null RFC3339 timestamp")
			return
		}
		v := p.UTC()
		fields.StartAt = &v
	}
	if v, ok := raw["end_at"]; ok {
		var p *time.Time
		if err := json.Unmarshal(v, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid end_at")
			return
		}
		v := utcPtr(p)
		fields.EndAt = &v
	}
	if v, ok := raw["all_day"]; ok {
		var p *bool
		if err := json.Unmarshal(v, &p); err != nil || p == nil {
			writeError(w, http.StatusBadRequest, "all_day must be a non-null boolean")
			return
		}
		fields.AllDay = p
	}
	if v, ok := raw["timezone"]; ok {
		var zone *string
		if err := json.Unmarshal(v, &zone); err != nil || zone == nil {
			writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
			return
		}
		if _, err := time.LoadLocation(*zone); err != nil {
			writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
			return
		}
		fields.Timezone = zone
	}
	if _, hasLocalStart := raw["local_start_at"]; hasLocalStart {
		var local *string
		if _, both := raw["start_at"]; both || json.Unmarshal(raw["local_start_at"], &local) != nil || local == nil || fields.Timezone == nil {
			writeError(w, http.StatusBadRequest, "local_start_at requires timezone and a local timestamp")
			return
		}
		fold := readString(raw["start_fold"])
		resolved, err := resolveLocalDateTime(*local, *fields.Timezone, fold)
		if err != nil {
			writeTimeInputError(w, err)
			return
		}
		fields.StartAt = &resolved
	}
	if _, hasLocalEnd := raw["local_end_at"]; hasLocalEnd {
		var local *string
		if _, both := raw["end_at"]; both || json.Unmarshal(raw["local_end_at"], &local) != nil || local == nil || fields.Timezone == nil {
			writeError(w, http.StatusBadRequest, "local_end_at requires timezone and a local timestamp")
			return
		}
		fold := readString(raw["end_fold"])
		resolved, err := resolveLocalDateTime(*local, *fields.Timezone, fold)
		if err != nil {
			writeTimeInputError(w, err)
			return
		}
		end := &resolved
		fields.EndAt = &end
	}
	if _, hasDateStart := raw["start_date"]; hasDateStart {
		var startDate, endDate *string
		if json.Unmarshal(raw["start_date"], &startDate) != nil || json.Unmarshal(raw["end_date"], &endDate) != nil || startDate == nil || endDate == nil || fields.Timezone == nil {
			writeError(w, http.StatusBadRequest, "start_date and end_date require timezone and exclusive date values")
			return
		}
		start, startErr := dateAtTimezoneMidnight(*startDate, *fields.Timezone)
		end, endErr := dateAtTimezoneMidnight(*endDate, *fields.Timezone)
		if startErr != nil || endErr != nil || !end.After(start) {
			writeError(w, http.StatusBadRequest, "invalid all-day date range")
			return
		}
		fields.StartDate, fields.EndDate = &startDate, &endDate
		fields.StartAt = &start
		endAt := &end
		fields.EndAt = &endAt
		allDay := true
		fields.AllDay = &allDay
	}
	if v, ok := raw["status"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil || p == nil || !validStatuses[*p] {
			writeError(w, http.StatusBadRequest, "status must be one of confirmed, tentative, cancelled")
			return
		}
		fields.Status = p
	}
	if message := scheduleTextInputError(derefStringPatch(fields.Description), derefStringPatch(fields.Location)); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}

	updated, err := h.svc.UpdateSchedule(r.Context(), userID, id, fields, key, requestHash)
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if errors.Is(err, service.ErrRevisionConflict) {
		writeError(w, http.StatusPreconditionFailed, "schedule changed; reload and retry")
		return
	}
	if errors.Is(err, service.ErrAccountDeleted) {
		writeError(w, http.StatusGone, "account has been deleted")
		return
	}
	if errors.Is(err, service.ErrInvalidSchedule) {
		writeError(w, http.StatusBadRequest, "end_at must be at least 1ms after start_at")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update schedule")
		return
	}
	writeScheduleJSON(w, http.StatusOK, updated)
}

func readString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func writeScheduleJSON(w http.ResponseWriter, status int, schedule *service.Schedule) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(schedule.Revision, 10)+`"`)
	writeJSON(w, status, toScheduleResponse(schedule))
}

func parseIfMatch(value string) (int64, bool) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, false
	}
	revision, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	return revision, err == nil && revision > 0
}

func resolveScheduleTimes(startAt, endAt *time.Time, localStartAt, localEndAt, startDate, endDate *string, allDay bool, timezone, startFold, endFold string) (time.Time, *time.Time, *string, *string, error) {
	if allDay && (startDate != nil || endDate != nil) {
		if startDate == nil || endDate == nil || startAt != nil || localStartAt != nil || endAt != nil || localEndAt != nil {
			return time.Time{}, nil, nil, nil, errInvalidTimeFields
		}
		start, err := dateAtTimezoneMidnight(*startDate, timezone)
		if err != nil {
			return time.Time{}, nil, nil, nil, errInvalidTimeFields
		}
		end, err := dateAtTimezoneMidnight(*endDate, timezone)
		if err != nil || !end.After(start) {
			return time.Time{}, nil, nil, nil, errInvalidTimeFields
		}
		return start, &end, startDate, endDate, nil
	}
	if startDate != nil || endDate != nil {
		return time.Time{}, nil, nil, nil, errInvalidTimeFields
	}
	if localStartAt != nil || localEndAt != nil {
		if startAt != nil || (localStartAt == nil) || (endAt != nil && localEndAt != nil) {
			return time.Time{}, nil, nil, nil, errInvalidTimeFields
		}
		start, err := resolveLocalDateTime(*localStartAt, timezone, startFold)
		if err != nil {
			return time.Time{}, nil, nil, nil, err
		}
		var end *time.Time
		if localEndAt != nil {
			resolved, err := resolveLocalDateTime(*localEndAt, timezone, endFold)
			if err != nil {
				return time.Time{}, nil, nil, nil, err
			}
			end = &resolved
		} else if endAt != nil {
			v := endAt.UTC()
			end = &v
		}
		return start, end, nil, nil, nil
	}
	if startAt == nil {
		return time.Time{}, nil, nil, nil, errInvalidTimeFields
	}
	start := startAt.UTC()
	var end *time.Time
	if endAt != nil {
		v := endAt.UTC()
		end = &v
	}
	if allDay {
		zone, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, nil, nil, nil, errInvalidTimeFields
		}
		localStart := start.In(zone)
		sd := localStart.Format("2006-01-02")
		ed := localStart.AddDate(0, 0, 1).Format("2006-01-02")
		if end != nil {
			endLocal := end.In(zone)
			ed = endLocal.Format("2006-01-02")
			if ed <= sd {
				endLocal = localStart.AddDate(0, 0, 1)
				ed = endLocal.Format("2006-01-02")
			}
		}
		localStartAt, _ := dateAtTimezoneMidnight(sd, timezone)
		localEndAt, _ := dateAtTimezoneMidnight(ed, timezone)
		return localStartAt, &localEndAt, &sd, &ed, nil
	}
	if startFold != "" || endFold != "" {
		return time.Time{}, nil, nil, nil, errInvalidTimeFields
	}
	return start, end, nil, nil, nil
}

func writeTimeInputError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNonexistentLocalTime):
		writeError(w, http.StatusUnprocessableEntity, "local time does not exist in timezone")
	case errors.Is(err, errAmbiguousLocalTime):
		writeError(w, http.StatusUnprocessableEntity, "local time is ambiguous; choose a fold")
	default:
		writeError(w, http.StatusBadRequest, "invalid schedule time fields")
	}
}

func (h *Handler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}

	key, validKey := idempotencyKey(r)
	if !validKey {
		writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	}
	err = h.svc.DeleteSchedule(r.Context(), userID, id, key)
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if errors.Is(err, service.ErrAccountDeleted) {
		writeError(w, http.StatusGone, "account has been deleted")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete schedule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func idempotencyKey(r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	return key, len(key) <= 128 && strings.TrimSpace(key) == key && !strings.ContainsAny(key, "\r\n\t")
}

func (h *Handler) BulkDeleteSchedules(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req bulkDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := h.val.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ids := make([]uuid.UUID, 0, len(req.IDs))
	seenIDs := make(map[uuid.UUID]struct{}, len(req.IDs))
	for _, s := range req.IDs {
		id, err := uuid.Parse(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id in ids")
			return
		}
		if _, exists := seenIDs[id]; exists {
			writeError(w, http.StatusBadRequest, "ids must be unique")
			return
		}
		seenIDs[id] = struct{}{}
		ids = append(ids, id)
	}
	key, validKey := idempotencyKey(r)
	if !validKey {
		writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	}
	sortedIDs := append([]string(nil), req.IDs...)
	sort.Strings(sortedIDs)
	canonical, err := json.Marshal(sortedIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return
	}
	requestHash := sha256.Sum256(canonical)

	n, err := h.svc.BulkDeleteSchedules(r.Context(), userID, ids, key, requestHash)
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "one or more schedules not found")
		return
	}
	if errors.Is(err, service.ErrAccountDeleted) {
		writeError(w, http.StatusGone, "account has been deleted")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to bulk delete schedules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}
