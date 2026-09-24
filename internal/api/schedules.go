package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

// listFrom / listTo bound an unset from/to query filter to a range wide
// enough to cover every value MySQL's DATETIME(3) can store.
var (
	listFrom = time.Unix(0, 0).UTC()
	listTo   = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
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
	if message := scheduleInputError(req.Description, req.Location, req.StartAt, req.EndAt); message != "" {
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

	sch, err := h.svc.CreateSchedule(r.Context(), userID, service.CreateScheduleInput{
		IdempotencyKey: key,
		Title:          req.Title,
		Description:    req.Description,
		Location:       req.Location,
		StartAt:        req.StartAt.UTC(),
		EndAt:          utcPtr(req.EndAt),
		AllDay:         req.AllDay,
		Status:         req.Status,
		Source:         req.Source,
		ExtractionID:   extractionID,
		Reminders:      reminders,
	})
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "Idempotency-Key reused with different content")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create schedule")
		return
	}
	writeJSON(w, http.StatusCreated, toScheduleResponse(sch))
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
	writeJSON(w, http.StatusOK, toScheduleResponse(sch))
}

func (h *Handler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	from := listFrom
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from (want RFC3339)")
			return
		}
		from = t.UTC()
	}

	to := listTo
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to (want RFC3339)")
			return
		}
		to = t.UTC()
	}

	var status *string
	if v := r.URL.Query().Get("status"); v != "" {
		if !validStatuses[v] {
			writeError(w, http.StatusBadRequest, "invalid status")
			return
		}
		status = &v
	}

	schedules, err := h.svc.ListSchedules(r.Context(), userID, from, to, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list schedules")
		return
	}

	resp := make([]scheduleResponse, 0, len(schedules))
	for _, s := range schedules {
		resp = append(resp, toScheduleResponse(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": resp})
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
	canonical, err := json.Marshal(raw)
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
		writeJSON(w, http.StatusOK, toScheduleResponse(replay))
		return
	}

	current, err := h.svc.GetSchedule(r.Context(), userID, id)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load schedule")
		return
	}

	fields := service.ScheduleFields{
		Title:       current.Title,
		Description: current.Description,
		Location:    current.Location,
		StartAt:     current.StartAt,
		EndAt:       current.EndAt,
		AllDay:      current.AllDay,
		Status:      current.Status,
	}

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
		fields.Title = title
	}
	if v, ok := raw["description"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid description")
			return
		}
		fields.Description = p
	}
	if v, ok := raw["location"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid location")
			return
		}
		fields.Location = p
	}
	if v, ok := raw["start_at"]; ok {
		var p *time.Time
		if err := json.Unmarshal(v, &p); err != nil || p == nil {
			writeError(w, http.StatusBadRequest, "start_at must be a non-null RFC3339 timestamp")
			return
		}
		fields.StartAt = p.UTC()
	}
	if v, ok := raw["end_at"]; ok {
		var p *time.Time
		if err := json.Unmarshal(v, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid end_at")
			return
		}
		fields.EndAt = utcPtr(p)
	}
	if v, ok := raw["all_day"]; ok {
		var p *bool
		if err := json.Unmarshal(v, &p); err != nil || p == nil {
			writeError(w, http.StatusBadRequest, "all_day must be a non-null boolean")
			return
		}
		fields.AllDay = *p
	}
	if v, ok := raw["status"]; ok {
		var p *string
		if err := json.Unmarshal(v, &p); err != nil || p == nil || !validStatuses[*p] {
			writeError(w, http.StatusBadRequest, "status must be one of confirmed, tentative, cancelled")
			return
		}
		fields.Status = *p
	}
	if message := scheduleInputError(fields.Description, fields.Location, fields.StartAt, fields.EndAt); message != "" {
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
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update schedule")
		return
	}
	writeJSON(w, http.StatusOK, toScheduleResponse(updated))
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
	for _, s := range req.IDs {
		id, err := uuid.Parse(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id in ids")
			return
		}
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
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to bulk delete schedules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}
