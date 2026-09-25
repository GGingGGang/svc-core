package api

import (
	"context"
	"net/http"
	"time"

	openapi "github.com/GGingGGang/svc-core/api"
)

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.readiness == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready"}`))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := h.readiness(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// status separates schedule availability from delayed NATS handoff. The
// followup field never claims Batch processing or reminder delivery.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if h.readiness == nil || h.readiness(ctx) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"schedules": "unavailable", "followup": "delayed",
		})
		return
	}
	followup := "delayed"
	if h.followup != nil {
		if available, err := h.followup(ctx); err == nil && available {
			followup = "available"
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"schedules": "available", "followup": followup,
	})
}

func serveOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapi.Spec)
}
