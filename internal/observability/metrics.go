package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// httpRequestDurationBuckets matches ../../metrics-spec.md §7.1.1 (HTTP
// server/client latency buckets), kept identical so P95 stays comparable
// across services even though this service exposes it via a raw Prometheus
// counter/histogram rather than the OTel Metrics SDK (../../PLAN.md §8.2's
// minimum bar is RED + the domain counter).
var httpRequestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_server_requests_total",
		Help: "Total HTTP requests handled, labeled by method/route/status.",
	}, []string{"method", "route", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_server_request_duration_seconds",
		Help:    "HTTP request duration in seconds, labeled by method/route/status.",
		Buckets: httpRequestDurationBuckets,
	}, []string{"method", "route", "status"})

	// DomainEventPublishedTotal / DomainEventPublishFailedTotal back the
	// domain_event_published_total{subject} counter required by
	// ../../PLAN.md §8.2 (plus its failure counterpart named in
	// ../../svc-core/PLAN.md §7). internal/events increments these on every
	// publish attempt.
	DomainEventPublishedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "domain_event_published_total",
		Help: "Total schedule domain events successfully published to NATS, labeled by subject.",
	}, []string{"subject"})

	DomainEventPublishFailedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "domain_event_publish_failed_total",
		Help: "Total schedule domain event publish attempts that failed, labeled by subject.",
	}, []string{"subject"})

	AIExternalRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_external_requests_total",
		Help: "Gemini HTTP attempts, including automatic retries, by status.",
	}, []string{"status"})

	ScheduleMutationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "schedule_mutations_total",
		Help: "Committed schedule creations, cancellation transitions, and deletions. Idempotency replays do not count.",
	}, []string{"operation"})

	AIExtractionRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_extraction_requests_total",
		Help: "Admitted user extraction requests; automatic Gemini retries count once.",
	})
)

// HTTPMetrics records the RED metrics (rate via *_total, errors via the
// status label, duration via the histogram) for every request. It must wrap
// the whole router (mounted with r.Use before routes are declared) so that
// by the time it reads the route pattern after next.ServeHTTP returns, chi
// has already resolved it — keeping the `route` label a bounded template
// ("/schedules/{id}") instead of a raw, unbounded path.
func HTTPMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(ww.Status())
		httpRequestsTotal.WithLabelValues(r.Method, route, status).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route, status).Observe(time.Since(start).Seconds())
	})
}
