package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

// maxExtractTextChars is the /schedules/extract input limit (../../PLAN.md
// §6) — text over this is rejected with 413 before any Gemini call.
const maxExtractTextChars = 4000

func (h *Handler) ExtractSchedules(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req extractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := h.val.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Text) > maxExtractTextChars {
		writeError(w, http.StatusRequestEntityTooLarge, "text exceeds 4000 characters")
		return
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeError(w, http.StatusBadRequest, "invalid timezone")
		return
	}

	candidates, err := h.svc.ExtractSchedules(r.Context(), userID, service.ExtractInput{
		Text:     req.Text,
		Now:      req.Now.UTC(),
		Timezone: req.Timezone,
		APIKey:   r.Header.Get("X-Gemini-Key"),
	})
	if err != nil {
		var rl *service.ExtractRateLimitedError
		if errors.As(err, &rl) {
			w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter.Seconds())))
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		writeError(w, http.StatusBadGateway, "extraction failed")
		return
	}
	writeJSON(w, http.StatusOK, toExtractResponse(candidates))
}
