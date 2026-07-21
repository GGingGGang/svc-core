package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
)

func Router(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz)
	r.Handle("/metrics", promhttp.Handler())
	r.Get("/openapi.yaml", serveOpenAPISpec)

	r.Route("/schedules", func(r chi.Router) {
		r.Use(authmw.TempUserID)

		r.Post("/", h.CreateSchedule)
		r.Get("/", h.ListSchedules)
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
