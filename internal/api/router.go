package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/GGingGGang/svc-core/internal/observability"
)

// Router builds the HTTP handler tree. authMiddleware guards every
// /schedules* route (JWKS-verified bearer JWT in production —
// internal/middleware.JWTAuth.Middleware — or a test double in tests); it is
// injected rather than hardcoded so tests can point auth at a local JWKS
// fixture without touching this file.
func Router(h *Handler, authMiddleware func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(observability.HTTPMetrics)
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz)
	r.Handle("/metrics", promhttp.Handler())
	r.Get("/openapi.yaml", serveOpenAPISpec)

	r.Route("/schedules", func(r chi.Router) {
		r.Use(authMiddleware)

		r.Post("/", h.CreateSchedule)
		r.Get("/", h.ListSchedules)
		r.Post("/extract", h.ExtractSchedules)
		r.Post("/bulk-delete", h.BulkDeleteSchedules)

		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.GetSchedule)
			r.Patch("/", h.UpdateSchedule)
			r.Delete("/", h.DeleteSchedule)

			r.Get("/reminders", h.ListReminders)
			r.Post("/reminders", h.AddReminder)
			r.Delete("/reminders/{reminderId}", h.DeleteReminder)
		})
	})

	return r
}
