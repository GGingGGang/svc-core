package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

// maxExtractTextChars is the /schedules/extract input limit (../../PLAN.md
// §6) — text over this is rejected with 413 before any Gemini call.
const maxExtractTextChars = 10000
const maxExtractTextBytes = 64 << 10
const maxExtractBodyBytes = 128 << 10

func (h *Handler) ExtractSchedules(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req extractRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxExtractBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := h.val.Struct(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text must not be blank")
		return
	}
	if utf8.RuneCountInString(req.Text) > maxExtractTextChars || len(req.Text) > maxExtractTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "text exceeds 10000 characters or 64KiB")
		return
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeError(w, http.StatusBadRequest, "invalid timezone")
		return
	}

	candidates, truncated, err := h.svc.ExtractSchedules(r.Context(), userID, service.ExtractInput{
		Text:     req.Text,
		Now:      req.Now.UTC(),
		Timezone: req.Timezone,
		APIKey:   r.Header.Get("X-Gemini-Key"),
	})
	if err != nil {
		if errors.Is(err, service.ErrExtractKeyUnavailable) {
			writeError(w, http.StatusBadGateway, "ai_key_unavailable")
			return
		}
		var rl *service.ExtractRateLimitedError
		if errors.As(err, &rl) {
			w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter.Seconds())))
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		writeError(w, http.StatusBadGateway, "extraction failed")
		return
	}
	writeJSON(w, http.StatusOK, toExtractResponse(candidates, truncated))
}
