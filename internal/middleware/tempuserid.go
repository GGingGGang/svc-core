// Package middleware holds cross-cutting HTTP middleware.
//
// TempUserID is the 1M placeholder auth: it trusts an X-User-Id header
// verbatim instead of validating a signed token (cluster-internal only, no
// HTTPRoute exposure). ./PLAN.md §5 calls for this to be replaced wholesale
// by JWKS validation in turn 2M. Everything the temporary scheme needs —
// the context key, the middleware, and the accessor handlers call — lives
// in this one file so the 2M swap is a delete-this-file-and-add-jwks.go
// change; the replacement only needs to keep the UserID(ctx) signature.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

type contextKey int

const userIDContextKey contextKey = iota

// TempUserID rejects requests missing a valid X-User-Id UUID with 401, and
// otherwise injects the parsed user id into the request context.
func TempUserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("X-User-Id")
		if raw == "" {
			unauthorized(w)
			return
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			unauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), userIDContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}

// UserID returns the authenticated user id placed in the context by the
// active auth middleware. Handlers depend on this function, not on which
// middleware populated it.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDContextKey).(uuid.UUID)
	return id, ok
}
