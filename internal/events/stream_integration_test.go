//go:build integration

// Integration tests spin up a real NATS server (testcontainers-go, JetStream
// enabled) and exercise stream declaration and publishing end to end: exact
// (non-wildcard) subject list, idempotent create-or-update, the
// Nats-Msg-Id/Content-Type/traceparent headers required by ../../PLAN.md
// §7.1, and server-side dedup. Requires a running Docker daemon; run with
// `go test -tags=integration ./...`.
package events_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/GGingGGang/svc-core/internal/events"
)

func setupJetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	ctx := context.Background()

	container, err := tcnats.Run(ctx, "nats:2.10-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})

	url, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	_, js, err := events.Connect(url)
	require.NoError(t, err)

	require.NoError(t, events.EnsureStream(ctx, js))
	return js
}

func TestEnsureStream_DeclaresExactSubjectsIdempotently(t *testing.T) {
	js := setupJetStream(t)
	ctx := context.Background()

	// create-or-update must be safe to call again (../../PLAN.md §7.2).
	require.NoError(t, events.EnsureStream(ctx, js))

	stream, err := js.Stream(ctx, events.StreamName)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)

	require.ElementsMatch(t, []string{
		"app.schedules.created.v1",
		"app.schedules.updated.v1",
		"app.schedules.deleted.v1",
	}, info.Config.Subjects, "subjects must be listed explicitly, never a app.schedules.> wildcard")
	require.Equal(t, jetstream.FileStorage, info.Config.Storage)
	require.Equal(t, 1, info.Config.Replicas)
	require.Equal(t, 7*24*time.Hour, info.Config.MaxAge)
	require.Equal(t, int64(1<<30), info.Config.MaxBytes)
	require.Equal(t, jetstream.DiscardOld, info.Config.Discard)
}

func TestPublisher_HeadersDedupAndPayload(t *testing.T) {
	js := setupJetStream(t)
	ctx := context.Background()
	pub := events.NewPublisher(js)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	tracedCtx := trace.ContextWithRemoteSpanContext(ctx, sc)

	occurredAt := time.Date(2026, 7, 23, 6, 0, 0, 0, time.UTC)
	evt := events.ScheduleEvent{
		ScheduleID: "018f0000-0000-7000-8000-000000000001",
		UserID:     "018f0000-0000-7000-8000-000000000002",
		Title:      "회의",
		StartAt:    occurredAt.Add(time.Hour),
		AllDay:     false,
		Source:     "manual",
		Reminders:  []events.ReminderSnapshot{{MinutesBefore: 30, Channel: "push"}},
		OccurredAt: occurredAt,
	}

	require.NoError(t, pub.PublishScheduleCreated(tracedCtx, evt))
	// A retry of the same logical event (identical schedule_id+occurred_at)
	// must be deduped by the server via Nats-Msg-Id (../../PLAN.md §7.1).
	require.NoError(t, pub.PublishScheduleCreated(tracedCtx, evt))

	stream, err := js.Stream(ctx, events.StreamName)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs, "duplicate Nats-Msg-Id must be deduped, not stored twice")

	cons, err := js.OrderedConsumer(ctx, events.StreamName, jetstream.OrderedConsumerConfig{})
	require.NoError(t, err)
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	require.NoError(t, err)

	require.Equal(t, events.SubjectScheduleCreated, msg.Subject())
	require.Equal(t, "application/json", msg.Headers().Get("Content-Type"))
	require.Equal(t,
		events.SubjectScheduleCreated+":"+evt.ScheduleID+":"+occurredAt.Format(time.RFC3339Nano)+":"+fmt.Sprintf("%x", sha256.Sum256(msg.Data())),
		msg.Headers().Get("Nats-Msg-Id"))
	require.Equal(t, "00-"+sc.TraceID().String()+"-"+sc.SpanID().String()+"-01", msg.Headers().Get("traceparent"))

	var decoded events.ScheduleEvent
	require.NoError(t, json.Unmarshal(msg.Data(), &decoded))
	require.Equal(t, evt.ScheduleID, decoded.ScheduleID)
	require.Equal(t, evt.UserID, decoded.UserID)
	require.Equal(t, evt.Title, decoded.Title)
	require.Equal(t, evt.Source, decoded.Source)
	require.Len(t, decoded.Reminders, 1)
	require.Equal(t, int32(30), decoded.Reminders[0].MinutesBefore)
	require.Equal(t, "push", decoded.Reminders[0].Channel)

	// A different revision at the same millisecond must not share the first
	// event's JetStream deduplication id.
	evt.Title = "변경된 회의"
	evt.Revision++
	require.NoError(t, pub.PublishScheduleCreated(tracedCtx, evt))
	info, err = stream.Info(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, info.State.Msgs)
	msg, err = cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(msg.Data(), &decoded))
	require.Equal(t, evt.Title, decoded.Title)
	require.Equal(t, evt.Revision, decoded.Revision)
}

func TestPublisher_UpdatedAndDeletedUseDistinctSubjects(t *testing.T) {
	js := setupJetStream(t)
	ctx := context.Background()
	pub := events.NewPublisher(js)

	occurredAt := time.Now().UTC()

	require.NoError(t, pub.PublishScheduleUpdated(ctx, events.ScheduleEvent{
		ScheduleID: "018f0000-0000-7000-8000-0000000000a1",
		UserID:     "018f0000-0000-7000-8000-0000000000a2",
		Title:      "수정됨",
		StartAt:    occurredAt,
		Source:     "manual",
		OccurredAt: occurredAt,
	}))
	require.NoError(t, pub.PublishScheduleDeleted(ctx, events.ScheduleDeletedEvent{
		ScheduleID: "018f0000-0000-7000-8000-0000000000b1",
		UserID:     "018f0000-0000-7000-8000-0000000000a2",
		OccurredAt: occurredAt,
	}))

	cons, err := js.OrderedConsumer(ctx, events.StreamName, jetstream.OrderedConsumerConfig{})
	require.NoError(t, err)

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
		require.NoError(t, err)
		seen[msg.Subject()] = true
	}
	require.True(t, seen[events.SubjectScheduleUpdated])
	require.True(t, seen[events.SubjectScheduleDeleted])
}
