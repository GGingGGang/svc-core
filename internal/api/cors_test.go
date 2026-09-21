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
}
