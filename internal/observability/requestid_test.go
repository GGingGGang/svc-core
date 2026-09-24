package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestWithRequestID(t *testing.T) {
	h := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Fatal("request id missing from context")
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/private-path", nil))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.Code)
	}
	if _, err := uuid.Parse(resp.Header().Get("X-Request-ID")); err != nil {
		t.Fatalf("invalid request id: %v", err)
	}
}
