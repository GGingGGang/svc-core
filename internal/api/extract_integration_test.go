//go:build integration

// POST /schedules/extract end to end against a real MySQL container: a
// happy-path call must return candidates and persist an ai_extractions
// audit row, oversized input must never reach Gemini, and a Gemini upstream
// stuck on 429 must surface as 429 + Retry-After to the caller with its own
// audit row recorded (../../PLAN.md §6, ./PLAN.md §8 4M DoD). Gemini itself
// is never called for real — a local httptest server stands in, matching
// the shape internal/ai/gemini_test.go already exercises at the unit level.
package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/GGingGGang/svc-core/internal/ai"
	"github.com/GGingGGang/svc-core/internal/observability"
)

func newGeminiStub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func geminiSuccessBody(t *testing.T, title string) []byte {
	t.Helper()
	inner := map[string]any{
		"candidates": []map[string]any{
			{
				"title":              title,
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

func TestExtractIntegration(t *testing.T) {
	var geminiCalls int32
	gemini := newGeminiStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&geminiCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(geminiSuccessBody(t, "통합테스트 회의"))
	})
	aiClnt := ai.New(gemini.URL, "gemini-test", "server-key")

	srv, jwks, db := setupServerWithPublisher(t, nil, aiClnt)
	client := srv.Client()
	token := jwks.mint(t, uuid.New().String(), time.Hour)

	t.Run("happy path returns candidates and records a success audit row", func(t *testing.T) {
		status, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/extract", token, map[string]any{
			"text":     "회의 다음주 화요일 오후 3시 강남역",
			"now":      "2026-06-28T00:00:00Z",
			"timezone": "Asia/Seoul",
		})
		require.Equal(t, http.StatusOK, status, string(body))

		var resp struct {
			Candidates []struct {
				Title             string     `json:"title"`
				Confidence        float64    `json:"confidence"`
				StartAt           *time.Time `json:"start_at"`
				NeedsConfirmation bool       `json:"needs_confirmation"`
				Issues            []string   `json:"issues"`
			} `json:"candidates"`
			Truncated bool `json:"truncated"`
		}
		require.NoError(t, json.Unmarshal(body, &resp))
		require.Len(t, resp.Candidates, 1)
		require.Equal(t, "통합테스트 회의", resp.Candidates[0].Title)
		require.InDelta(t, 0.9, resp.Candidates[0].Confidence, 0.0001)
		require.NotNil(t, resp.Candidates[0].StartAt)
		require.False(t, resp.Candidates[0].NeedsConfirmation)
		require.Empty(t, resp.Candidates[0].Issues)
		require.False(t, resp.Truncated)

		require.Eventually(t, func() bool {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM ai_extractions WHERE status = 'success'").Scan(&count); err != nil {
				return false
			}
			return count == 1
		}, 5*time.Second, 100*time.Millisecond, "a success ai_extractions row should be recorded")
		var schedules int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM schedules").Scan(&schedules))
		require.Zero(t, schedules, "extraction alone must not save a schedule")
	})

	t.Run("text over 10000 Unicode chars is rejected before calling gemini", func(t *testing.T) {
		before := atomic.LoadInt32(&geminiCalls)
		status, _ := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/extract", token, map[string]any{
			"text":     strings.Repeat("가", 10001),
			"now":      "2026-06-28T00:00:00Z",
			"timezone": "UTC",
		})
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
		require.Equal(t, before, atomic.LoadInt32(&geminiCalls), "oversized text must never reach gemini")
	})

	t.Run("blank text is rejected before calling gemini", func(t *testing.T) {
		before := atomic.LoadInt32(&geminiCalls)
		status, _ := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/extract", token, map[string]any{
			"text": "  \n\t ", "now": "2026-06-28T00:00:00Z", "timezone": "UTC",
		})
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, before, atomic.LoadInt32(&geminiCalls))
	})

	t.Run("oversized request body is rejected before calling gemini", func(t *testing.T) {
		before := atomic.LoadInt32(&geminiCalls)
		status, _ := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/extract", token, map[string]any{
			"text": strings.Repeat("a", 130000), "now": "2026-06-28T00:00:00Z", "timezone": "UTC",
		})
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
		require.Equal(t, before, atomic.LoadInt32(&geminiCalls))
	})

	t.Run("invalid timezone is rejected", func(t *testing.T) {
		status, _ := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/extract", token, map[string]any{
			"text":     "회의",
			"now":      "2026-06-28T00:00:00Z",
			"timezone": "Not/AZone",
		})
		require.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("missing bearer token is unauthorized", func(t *testing.T) {
		status, _ := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/extract", "", map[string]any{
			"text": "회의", "now": "2026-06-28T00:00:00Z", "timezone": "UTC",
		})
		require.Equal(t, http.StatusUnauthorized, status)
	})
}

// TestExtractIntegration_RateLimited uses its own server + stub (an
// always-429 Gemini) so it doesn't share the happy-path server's audit-row
// count above.
func TestExtractIntegration_RateLimited(t *testing.T) {
	var upstreamCalls atomic.Int32
	gemini := newGeminiStub(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	aiClnt := ai.New(gemini.URL, "gemini-test", "server-key")

	srv, jwks, db := setupServerWithPublisher(t, nil, aiClnt)
	requestsBefore := testutil.ToFloat64(observability.AIExtractionRequestsTotal)
	externalBefore := testutil.ToFloat64(observability.AIExternalRequestsTotal.WithLabelValues("429"))
	client := srv.Client()
	token := jwks.mint(t, uuid.New().String(), time.Hour)

	reqBody, err := json.Marshal(map[string]any{
		"text":     "회의",
		"now":      "2026-06-28T00:00:00Z",
		"timezone": "UTC",
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/schedules/extract", bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "12", resp.Header.Get("Retry-After"))
	require.Equal(t, int32(2), upstreamCalls.Load())
	require.Equal(t, requestsBefore+1, testutil.ToFloat64(observability.AIExtractionRequestsTotal))
	require.Equal(t, externalBefore+2, testutil.ToFloat64(observability.AIExternalRequestsTotal.WithLabelValues("429")))

	require.Eventually(t, func() bool {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM ai_extractions WHERE status = 'failed'").Scan(&count); err != nil {
			return false
		}
		return count == 1
	}, 5*time.Second, 100*time.Millisecond, "a failed extraction must still be recorded")
}

func TestExtractIntegration_MissingKey(t *testing.T) {
	geminiCalls := 0
	gemini := newGeminiStub(t, func(http.ResponseWriter, *http.Request) { geminiCalls++ })
	srv, jwks, _ := setupServerWithPublisher(t, nil, ai.New(gemini.URL, "gemini-test", ""))
	token := jwks.mint(t, uuid.New().String(), time.Hour)
	status, body := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules/extract", token, map[string]any{
		"text": "meeting tomorrow", "now": "2026-06-28T00:00:00Z", "timezone": "UTC",
	})
	require.Equal(t, http.StatusBadGateway, status)
	require.JSONEq(t, `{"error":"ai_key_unavailable"}`, string(body))
	require.Zero(t, geminiCalls)
}

func TestExtractIntegration_UserLimit(t *testing.T) {
	var calls atomic.Int32
	gemini := newGeminiStub(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write(geminiSuccessBody(t, "meeting"))
	})
	srv, jwks, db := setupServerWithPublisher(t, nil, ai.New(gemini.URL, "gemini-test", "key"))
	token := jwks.mint(t, uuid.New().String(), time.Hour)
	request := map[string]any{"text": "meeting", "now": "2026-06-28T00:00:00Z", "timezone": "UTC"}
	for i := 0; i < 5; i++ {
		status, body := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules/extract", token, request)
		require.Equal(t, http.StatusOK, status, string(body))
	}
	status, body := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules/extract", token, request)
	require.Equal(t, http.StatusTooManyRequests, status, string(body))
	require.Equal(t, int32(5), calls.Load())
	var admissions int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM ai_request_admissions").Scan(&admissions))
	require.Equal(t, 5, admissions)
}
