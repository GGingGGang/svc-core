package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSPreflight(t *testing.T) {
	h := CORS("https://www.ggang.cloud")(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodOptions, "/schedules", nil)
	req.Header.Set("Origin", "https://www.ggang.cloud")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent || res.Header().Get("Access-Control-Allow-Origin") != "https://www.ggang.cloud" {
		t.Fatalf("unexpected CORS response: %d %q", res.Code, res.Header().Get("Access-Control-Allow-Origin"))
	}
	if got := res.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type, Idempotency-Key, X-Gemini-Key" {
		t.Fatalf("Idempotency-Key or X-Gemini-Key preflight is missing: %q", got)
	}
}

func TestCORSExposesErrorRequestID(t *testing.T) {
	h := CORS("https://www.ggang.cloud")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "test-id")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	req := httptest.NewRequest(http.MethodGet, "/schedules", nil)
	req.Header.Set("Origin", "https://www.ggang.cloud")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Header().Get("Access-Control-Expose-Headers") != "X-Request-ID" || res.Header().Get("X-Request-ID") != "test-id" {
		t.Fatalf("error request ID is not exposed to the browser: %v", res.Header())
	}
}
