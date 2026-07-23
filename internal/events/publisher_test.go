package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/observability"
)

// TestPublisher_NilJetStreamFailsFastAndCounts covers the no-Docker path: a
// Publisher built without a live JetStream connection (../../PLAN.md §7 —
// "발행은 best-effort") must return an error rather than panic, and must
// still increment the failure counter so the outage is observable.
func TestPublisher_NilJetStreamFailsFastAndCounts(t *testing.T) {
	pub := events.NewPublisher(nil)
	counter := observability.DomainEventPublishFailedTotal.WithLabelValues(events.SubjectScheduleCreated)
	before := testutil.ToFloat64(counter)

	err := pub.PublishScheduleCreated(context.Background(), events.ScheduleEvent{
		ScheduleID: "018f0000-0000-7000-8000-000000000001",
		UserID:     "018f0000-0000-7000-8000-000000000002",
		Title:      "회의",
		StartAt:    time.Now().UTC(),
		Source:     "manual",
		OccurredAt: time.Now().UTC(),
	})
	require.Error(t, err)

	require.Equal(t, before+1, testutil.ToFloat64(counter))
}
