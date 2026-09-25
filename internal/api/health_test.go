package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadyzRequiresDatabaseCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(context.Context) error
		want  int
	}{
		{name: "ready", check: func(context.Context) error { return nil }, want: http.StatusOK},
		{name: "database unavailable", check: func(context.Context) error { return errors.New("db unavailable") }, want: http.StatusServiceUnavailable},
		{name: "missing check", want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			(&Handler{readiness: tc.check}).readyz(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestHealthzDoesNotRequireDatabase(t *testing.T) {
	h := NewHandler(nil, func(context.Context) error {
		t.Fatal("liveness must not query the database")
		return nil
	})
	router := Router(h, func(next http.Handler) http.Handler { return next })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestStatusSeparatesStorageAndFollowup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readiness func(context.Context) error
		followup  func(context.Context) (bool, error)
		code      int
		body      string
	}{
		{"ready", func(context.Context) error { return nil }, func(context.Context) (bool, error) { return true, nil }, 200, `{"schedules":"available","followup":"available"}`},
		{"handoff delayed", func(context.Context) error { return nil }, func(context.Context) (bool, error) { return false, nil }, 200, `{"schedules":"available","followup":"delayed"}`},
		{"handoff check failed", func(context.Context) error { return nil }, func(context.Context) (bool, error) { return false, errors.New("db error") }, 200, `{"schedules":"available","followup":"delayed"}`},
		{"storage unavailable", func(context.Context) error { return errors.New("db error") }, nil, 503, `{"schedules":"unavailable","followup":"delayed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{readiness: tc.readiness, followup: tc.followup}
			w := httptest.NewRecorder()
			h.status(w, httptest.NewRequest(http.MethodGet, "/status", nil))
			require.Equal(t, tc.code, w.Code)
			require.JSONEq(t, tc.body, w.Body.String())
		})
	}
}
