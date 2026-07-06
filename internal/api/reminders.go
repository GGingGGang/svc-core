package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

func (h *Handler) ListReminders(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scheduleID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}

	reminders, err := h.svc.ListReminders(r.Context(), userID, scheduleID)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list reminders")
		return
	}

	resp := make([]reminderResponse, 0, len(reminders))
	for _, rem := range reminders {
		resp = append(resp, toReminderResponse(rem))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": resp})
}

func (h *Handler) AddReminder(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scheduleID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}

	var req reminderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := h.val.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rem, err := h.svc.AddReminder(r.Context(), userID, scheduleID, req.MinutesBefore, req.Channel)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add reminder")
		return
	}
	writeJSON(w, http.StatusCreated, toReminderResponse(rem))
}

func (h *Handler) DeleteReminder(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scheduleID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}
	reminderID, err := uuid.Parse(chi.URLParam(r, "reminderId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid reminder id")
		return
	}

	err = h.svc.DeleteReminder(r.Context(), userID, scheduleID, reminderID)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "reminder not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete reminder")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
