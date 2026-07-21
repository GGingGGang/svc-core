// Package observability wires the minimal OpenTelemetry tracing setup called
// for by ../PLAN.md §8.2: W3C TraceContext + Baggage propagation, and a
// tracer provider whose exporter is chosen entirely by env vars so the
// service starts safely whether or not a collector exists yet.
package observability

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupTracing installs the W3C TraceContext + Baggage propagator
// unconditionally (needed for incoming/outgoing header propagation
// regardless of whether traces are exported), then wires a TracerProvider
// only if OTEL_TRACES_EXPORTER=otlp is set.
//
// Any other value — including "none" or the env var being entirely unset —
// leaves otel's built-in no-op TracerProvider in place, which is the safe
// starting point named in ./PLAN.md's env template for when the collector
// isn't deployed yet: spans cost nothing beyond an ID, and nothing dials out.
// Set OTEL_TRACES_EXPORTER=otlp (plus OTEL_EXPORTER_OTLP_ENDPOINT /
// OTEL_EXPORTER_OTLP_PROTOCOL=grpc) once a collector exists; no code change
// is needed. Only grpc OTLP is wired since that's the only protocol the
// contract's env template specifies.
//
// Returns a shutdown func to flush/stop the provider on graceful shutdown.
func SetupTracing(ctx context.Context) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if os.Getenv("OTEL_TRACES_EXPORTER") != "otlp" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx, resource.WithFromEnv())
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
