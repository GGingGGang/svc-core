package observability

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		wrapped := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		next.ServeHTTP(wrapped, r)
		if wrapped.Status() >= 400 {
			log.Printf("request error: request_id=%s method=%s route=%s status=%d", id, r.Method, chi.RouteContext(r.Context()).RoutePattern(), wrapped.Status())
		}
	})
}
