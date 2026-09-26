package api

import (
	"context"
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
const extractProcessingTimeout = 15 * time.Second

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

	ctx, cancel := context.WithTimeout(r.Context(), extractProcessingTimeout)
	defer cancel()
	candidates, truncated, err := h.svc.ExtractSchedules(ctx, userID, service.ExtractInput{
		Text:     req.Text,
		Now:      req.Now.UTC(),
		Timezone: req.Timezone,
		APIKey:   r.Header.Get("X-Gemini-Key"),
	})
	if err != nil {
		if errors.Is(err, service.ErrExtractBusy) {
			writeError(w, http.StatusTooManyRequests, "extraction already in progress")
			return
		}
		if errors.Is(err, service.ErrExtractUserRateLimited) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "extraction request limit reached")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "extraction timed out")
			return
		}
		if errors.Is(err, service.ErrExtractKeyUnavailable) {
			writeError(w, http.StatusBadGateway, "ai_key_unavailable")
			return
		}
		if errors.Is(err, service.ErrExtractInvalidKey) {
			writeError(w, http.StatusBadGateway, "ai_key_invalid")
			return
		}
		if errors.Is(err, service.ErrExtractModelUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "ai_model_unavailable")
			return
		}
		if errors.Is(err, service.ErrExtractUpstreamUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "ai_upstream_unavailable")
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
