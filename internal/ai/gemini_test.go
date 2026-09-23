package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GGingGGang/svc-core/internal/ai"
)

func stubGeminiServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// successBody builds a generateContent envelope whose single candidate's
// text part is itself the JSON our responseSchema forces Gemini to produce
// (../../PLAN.md §5.2 candidates shape).
func successBody(t *testing.T) []byte {
	t.Helper()
	inner := map[string]any{
		"candidates": []map[string]any{
			{
				"title":              "회의",
				"start_at":           "2026-07-07T06:00:00Z",
				"end_at":             nil,
				"all_day":            false,
				"location":           "강남역",
				"description":        "",
				"confidence":         0.9,
				"needs_confirmation": false,
				"issues":             []string{},
			},
		},
	}
	innerJSON, err := json.Marshal(inner)
	require.NoError(t, err)

	envelope := map[string]any{
		"candidates": []map[string]any{
			{
				"content":      map[string]any{"parts": []map[string]any{{"text": string(innerJSON)}}},
				"finishReason": "STOP",
			},
		},
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	return body
}

func TestExtract_Success(t *testing.T) {
	var gotAuth string
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(successBody(t))
	})

	c := ai.New(srv.URL, "gemini-test", "")
	result, err := c.Extract(context.Background(), "byok-key", ai.ExtractInput{
		Text:     "회의 다음주 화요일 오후 3시 강남역",
		Now:      time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC),
		Timezone: "Asia/Seoul",
	})
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	require.Equal(t, "회의", result.Candidates[0].Title)
	require.Equal(t, "byok-key", gotAuth, "BYOK header must take priority over the server default key")
}

func TestExtract_InvalidCandidateNeedsConfirmation(t *testing.T) {
	inner, err := json.Marshal(map[string]any{"candidates": []map[string]any{
		{"title": "valid", "start_at": "2026-07-07T06:00:00Z", "all_day": false, "confidence": 0.9, "needs_confirmation": false, "issues": []string{}},
		{"title": "missing date", "start_at": nil, "all_day": false, "confidence": 0.8, "needs_confirmation": true, "issues": []string{"missing_start_at"}},
		{"title": "invalid date", "start_at": "tomorrow", "all_day": false, "confidence": 0.8, "needs_confirmation": false, "issues": []string{}},
	}})
	require.NoError(t, err)
	envelope, err := json.Marshal(map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{{"text": string(inner)}}}}}})
	require.NoError(t, err)
	srv := stubGeminiServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(envelope) })
	result, err := ai.New(srv.URL, "gemini-test", "key").Extract(context.Background(), "", ai.ExtractInput{Text: "three dates", Now: time.Now(), Timezone: "UTC"})
	require.NoError(t, err)
	require.Len(t, result.Candidates, 3)
	require.False(t, result.Candidates[0].NeedsConfirmation)
	require.NotNil(t, result.Candidates[0].StartAt)
	require.True(t, result.Candidates[1].NeedsConfirmation)
	require.Nil(t, result.Candidates[1].StartAt)
	require.Contains(t, result.Candidates[1].Issues, "missing_start_at")
	require.True(t, result.Candidates[2].NeedsConfirmation)
	require.Nil(t, result.Candidates[2].StartAt)
	require.Contains(t, result.Candidates[2].Issues, "invalid_start_at")
}

func TestExtract_CapsCandidates(t *testing.T) {
	candidates := make([]map[string]any, 21)
	for i := range candidates {
		candidates[i] = map[string]any{"title": "event", "start_at": "2026-07-07T06:00:00Z", "all_day": false, "confidence": 0.9, "needs_confirmation": false, "issues": []string{}}
	}
	inner, err := json.Marshal(map[string]any{"candidates": candidates})
	require.NoError(t, err)
	envelope, err := json.Marshal(map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{{"text": string(inner)}}}}}})
	require.NoError(t, err)
	srv := stubGeminiServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(envelope) })
	result, err := ai.New(srv.URL, "gemini-test", "key").Extract(context.Background(), "", ai.ExtractInput{Text: "many", Now: time.Now(), Timezone: "UTC"})
	require.NoError(t, err)
	require.Len(t, result.Candidates, 20)
	require.True(t, result.Truncated)
}

func TestExtract_FallsBackToServerKeyWhenNoBYOK(t *testing.T) {
	var gotAuth string
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(successBody(t))
	})

	c := ai.New(srv.URL, "gemini-test", "server-default-key")
	_, err := c.Extract(context.Background(), "", ai.ExtractInput{Text: "x", Now: time.Now(), Timezone: "UTC"})
	require.NoError(t, err)
	require.Equal(t, "server-default-key", gotAuth)
}

func TestExtract_MissingAPIKey(t *testing.T) {
	called := false
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	c := ai.New(srv.URL, "gemini-test", "")
	_, err := c.Extract(context.Background(), "", ai.ExtractInput{Text: "x", Now: time.Now(), Timezone: "UTC"})
	require.ErrorIs(t, err, ai.ErrMissingAPIKey)
	require.False(t, called, "no HTTP call should be made without a key (BYOK or fallback)")
}

// TestExtract_RetriesOnceThenSucceeds covers the 429-then-recover half of
// ../../PLAN.md §6's "429/5xx 시 지수 backoff 1회 후 재시도" contract.
func TestExtract_RetriesOnceThenSucceeds(t *testing.T) {
	var attempts int32
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(successBody(t))
	})

	c := ai.New(srv.URL, "gemini-test", "key")
	result, err := c.Extract(context.Background(), "", ai.ExtractInput{Text: "x", Now: time.Now(), Timezone: "UTC"})
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	require.Equal(t, int32(2), atomic.LoadInt32(&attempts))
}

// TestExtract_RateLimitedAfterRetryExhausted covers the "still 429 after the
// retry" half: exactly one retry happens (never unbounded), then the caller
// gets RateLimitedError carrying the upstream Retry-After so the handler can
// set it on the 429 it returns.
func TestExtract_RateLimitedAfterRetryExhausted(t *testing.T) {
	var attempts int32
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	c := ai.New(srv.URL, "gemini-test", "key")
	_, err := c.Extract(context.Background(), "", ai.ExtractInput{Text: "x", Now: time.Now(), Timezone: "UTC"})

	var rl *ai.RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 17*time.Second, rl.RetryAfter)
	require.Equal(t, int32(2), atomic.LoadInt32(&attempts), "exactly one retry, not unbounded")
}

func TestExtract_5xxIsRetriedSameAsRateLimit(t *testing.T) {
	var attempts int32
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	c := ai.New(srv.URL, "gemini-test", "key")
	_, err := c.Extract(context.Background(), "", ai.ExtractInput{Text: "x", Now: time.Now(), Timezone: "UTC"})

	var rl *ai.RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, int32(2), atomic.LoadInt32(&attempts))
}

func TestExtract_NonRetryableErrorFailsImmediately(t *testing.T) {
	var attempts int32
	srv := stubGeminiServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
	})

	c := ai.New(srv.URL, "gemini-test", "key")
	_, err := c.Extract(context.Background(), "", ai.ExtractInput{Text: "x", Now: time.Now(), Timezone: "UTC"})
	require.Error(t, err)

	var rl *ai.RateLimitedError
	require.False(t, errors.As(err, &rl), "a plain 400 must not be classified as rate limited")
	require.Equal(t, int32(1), atomic.LoadInt32(&attempts), "no retry for a non-retryable status")
}
