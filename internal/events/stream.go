package events

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Connect dials NATS and wraps the connection with a JetStream context. The
// connection retries indefinitely once established (server restarts,
// network blips) but the initial dial itself uses a bounded timeout so a
// NATS outage at startup does not hang this service forever — callers
// should treat a returned error as non-fatal (../PLAN.md §7 describes
// publishing as best-effort) and keep serving HTTP with a Publisher that has
// nothing to publish to yet.
func Connect(url string) (*nats.Conn, jetstream.JetStream, error) {
	nc, err := nats.Connect(url,
		nats.Name("svc-core"),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect nats: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("init jetstream: %w", err)
	}

	return nc, js, nil
}

// EnsureStream declares the APP_SCHEDULES stream idempotently
// (create-or-update — ../../PLAN.md §7.2, no human action required). It is
// called once at startup by the publishing service (core); the consuming
// service (batch) separately owns APP_SCHEDULES_DLQ and its durable
// consumer.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: StreamName,
		Subjects: []string{
			SubjectScheduleCreated,
			SubjectScheduleUpdated,
			SubjectScheduleDeleted,
		},
		Storage:  jetstream.FileStorage,
		Replicas: 1,
		MaxAge:   streamMaxAge,
		MaxBytes: streamMaxBytes,
		Discard:  jetstream.DiscardOld,
	})
	if err != nil {
		return fmt.Errorf("ensure %s stream: %w", StreamName, err)
	}
	return nil
}
