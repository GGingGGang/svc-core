// Package ai calls the Gemini API to turn free-form text into schedule
// candidates (../../PLAN.md §6). It has zero third-party dependencies —
// stdlib net/http only, per ./PLAN.md §2.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ErrMissingAPIKey is returned when neither the request's BYOK header nor
// the server's GEMINI_API_KEY fallback supplied a key (../../PLAN.md §6:
// "요청 헤더 X-Gemini-Key (BYOK) → env GEMINI_API_KEY").
var ErrMissingAPIKey = errors.New("gemini: no api key provided")

// RateLimitedError is returned once a 429/5xx response survives the single
// backoff-and-retry (./PLAN.md §6: "429/5xx 시 지수 backoff 1회 후 사용자에게
// 429 + Retry-After"). RetryAfter is always populated (falling back to a
// fixed default when Gemini's response didn't include one) so callers can
// set the client-facing Retry-After header directly.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("gemini: rate limited, retry after %s", e.RetryAfter)
}

const (
	requestTimeout     = 10 * time.Second
	backoffBeforeRetry = time.Second
	defaultRetryAfter  = 5 * time.Second
)

// Candidate mirrors the schedule candidate shape ../../PLAN.md §5.2
// documents for the /schedules/extract response — json tags match 1:1 so
// the service layer can pass a Candidate straight through to the HTTP
// response without remapping fields.
type Candidate struct {
	Title       string     `json:"title"`
	StartAt     time.Time  `json:"start_at"`
	EndAt       *time.Time `json:"end_at"`
	AllDay      bool       `json:"all_day"`
	Location    *string    `json:"location"`
	Description string     `json:"description"`
	Confidence  float64    `json:"confidence"`
}

// ExtractResult is the parsed, schema-validated model output.
type ExtractResult struct {
	Candidates []Candidate `json:"candidates"`
}

// ExtractInput is everything the prompt needs to turn relative time
// expressions ("다음주 화요일") into absolute UTC timestamps (../../PLAN.md §6).
type ExtractInput struct {
	Text     string
	Now      time.Time
	Timezone string
}

// Client calls the Gemini generateContent REST endpoint.
type Client struct {
	httpClient *http.Client
	baseURL    string
	model      string
	defaultKey string
}

// New builds a Client. defaultKey is the server-side GEMINI_API_KEY
// fallback (../../PLAN.md §6) — it may be empty, meaning every request must
// supply its own BYOK key.
func New(baseURL, model, defaultKey string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: requestTimeout},
		baseURL:    baseURL,
		model:      model,
		defaultKey: defaultKey,
	}
}

// Model returns the configured model name — the service layer records it on
// every ai_extractions row (../../svc-core/PLAN.md §6) without needing its
// own copy of the config.
func (c *Client) Model() string {
	return c.model
}

// Extract calls Gemini once, retrying exactly once (after a fixed backoff)
// on a 429 or 5xx response, per ../../PLAN.md §6. overrideKey is the
// request's X-Gemini-Key header value; an empty string falls back to the
// server's configured key. The key is never logged.
func (c *Client) Extract(ctx context.Context, overrideKey string, in ExtractInput) (*ExtractResult, error) {
	key := overrideKey
	if key == "" {
		key = c.defaultKey
	}
	if key == "" {
		return nil, ErrMissingAPIKey
	}

	body, err := buildRequestBody(in)
	if err != nil {
		return nil, fmt.Errorf("gemini: build request: %w", err)
	}

	result, err := c.attempt(ctx, key, body)
	if err == nil {
		return result, nil
	}
	if !isRetryable(err) {
		return nil, err
	}

	select {
	case <-time.After(backoffBeforeRetry):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	result, err = c.attempt(ctx, key, body)
	if err == nil {
		return result, nil
	}
	if !isRetryable(err) {
		return nil, err
	}

	retryAfter := defaultRetryAfter
	var upstream *upstreamStatusError
	if errors.As(err, &upstream) && upstream.retryAfter > 0 {
		retryAfter = upstream.retryAfter
	}
	return nil, &RateLimitedError{RetryAfter: retryAfter}
}

// upstreamStatusError is the internal sentinel used to decide retry
// eligibility (429/5xx) before it is either retried or turned into a
// RateLimitedError for the caller.
type upstreamStatusError struct {
	status     int
	retryAfter time.Duration
}

func (e *upstreamStatusError) Error() string {
	return fmt.Sprintf("gemini: upstream status %d", e.status)
}

func isRetryable(err error) bool {
	var upstream *upstreamStatusError
	if !errors.As(err, &upstream) {
		return false
	}
	return upstream.status == http.StatusTooManyRequests || upstream.status >= 500
}

func (c *Client) attempt(ctx context.Context, key string, body []byte) (*ExtractResult, error) {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", c.baseURL, c.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("gemini: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &upstreamStatusError{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}

	return parseResponse(respBody)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// geminiSchema is the subset of OpenAPI schema Gemini's responseSchema
// accepts (type/properties/items/required/nullable), used to force the
// model's JSON output into the exact candidates shape ../../PLAN.md §5.2
// documents.
type geminiSchema struct {
	Type        string                  `json:"type"`
	Properties  map[string]geminiSchema `json:"properties,omitempty"`
	Items       *geminiSchema           `json:"items,omitempty"`
	Required    []string                `json:"required,omitempty"`
	Nullable    bool                    `json:"nullable,omitempty"`
	Description string                  `json:"description,omitempty"`
}

var responseSchema = geminiSchema{
	Type:     "OBJECT",
	Required: []string{"candidates"},
	Properties: map[string]geminiSchema{
		"candidates": {
			Type: "ARRAY",
			Items: &geminiSchema{
				Type:     "OBJECT",
				Required: []string{"title", "start_at", "all_day", "confidence"},
				Properties: map[string]geminiSchema{
					"title":       {Type: "STRING"},
					"start_at":    {Type: "STRING", Description: "RFC3339 UTC timestamp"},
					"end_at":      {Type: "STRING", Nullable: true, Description: "RFC3339 UTC timestamp, or null if unknown"},
					"all_day":     {Type: "BOOLEAN"},
					"location":    {Type: "STRING", Nullable: true},
					"description": {Type: "STRING"},
					"confidence":  {Type: "NUMBER", Description: "0-1 confidence estimate"},
				},
			},
		},
	},
}

func buildRequestBody(in ExtractInput) ([]byte, error) {
	prompt := fmt.Sprintf(`You are a scheduling assistant. Extract every distinct calendar event mentioned in the text below into the "candidates" array.

Current time (RFC3339 UTC): %s
User timezone: %s

Convert every relative date/time expression (e.g. "다음주 화요일", "내일 오후 3시") into an absolute UTC RFC3339 timestamp, computed relative to the current time above and interpreted in the user's timezone. end_at is null if the text does not specify an end time. confidence is your estimate (0-1) of how confident you are in the extraction. If the text mentions no events, return an empty candidates array.

Text:
"""
%s
"""`, in.Now.UTC().Format(time.RFC3339), in.Timezone, in.Text)

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"responseSchema":   responseSchema,
		},
	}
	return json.Marshal(reqBody)
}

type generateContentResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
}

func parseResponse(body []byte) (*ExtractResult, error) {
	var raw generateContentResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("gemini: decode envelope: %w", err)
	}
	if len(raw.Candidates) == 0 || len(raw.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini: empty response (finishReason=%s)", firstFinishReason(raw))
	}

	text := raw.Candidates[0].Content.Parts[0].Text
	var result ExtractResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("gemini: decode structured output: %w", err)
	}
	return &result, nil
}

func firstFinishReason(raw generateContentResponse) string {
	if len(raw.Candidates) == 0 {
		return "unknown"
	}
	return raw.Candidates[0].FinishReason
}
