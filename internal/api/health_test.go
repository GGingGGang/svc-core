package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
