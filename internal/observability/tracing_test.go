package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestSamplerFromEnv(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
	decision := samplerFromEnv().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(), TraceID: trace.TraceID{1}, Name: "request", Kind: trace.SpanKindServer,
	})
	require.Equal(t, sdktrace.Drop, decision.Decision)

	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	decision = samplerFromEnv().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(), TraceID: trace.TraceID{1}, Name: "request", Kind: trace.SpanKindServer,
	})
	require.Equal(t, sdktrace.Drop, decision.Decision)
}

func TestServiceNameUsesEnvOrCoreDefault(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "core-test")
	require.Equal(t, "core-test", serviceName())
	t.Setenv("OTEL_SERVICE_NAME", "")
	require.Equal(t, "core", serviceName())
}

func TestParentBasedSamplerHonorsRemoteParent(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	parentCtx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1},
	}))
	decision := samplerFromEnv().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: parentCtx, TraceID: trace.TraceID{2}, Name: "request", Kind: trace.SpanKindServer,
	})
	require.Equal(t, sdktrace.Drop, decision.Decision)

	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	parentCtx = trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1},
	}))
	decision = samplerFromEnv().ShouldSample(sdktrace.SamplingParameters{
		ParentContext: parentCtx, TraceID: trace.TraceID{2}, Name: "request", Kind: trace.SpanKindServer,
	})
	require.Equal(t, sdktrace.RecordAndSample, decision.Decision)
}
