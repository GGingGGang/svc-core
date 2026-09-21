package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"github.com/GGingGGang/svc-core/internal/observability"
)

// natsHeaderCarrier adapts nats.Header to OTel's propagation.TextMapCarrier
// without going through propagation.HeaderCarrier: that type delegates to
// http.Header.Set/Get, which canonicalize keys (textproto rules turn
// "traceparent" into "Traceparent"). nats.Header — and the NATS wire
// protocol — is case-sensitive, so a canonicalized key would silently
// vanish from anything doing an exact-case lookup for the W3C-mandated
// lowercase "traceparent"/"tracestate"/"baggage" names.
type natsHeaderCarrier nats.Header

func (c natsHeaderCarrier) Get(key string) string {
	if v := c[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (c natsHeaderCarrier) Set(key, value string) {
	c[key] = []string{value}
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// Publisher publishes schedule domain events to the APP_SCHEDULES stream.
// js may be nil (NATS unreachable when the service started); publish calls
// then fail fast rather than panic, matching the best-effort contract in
// ../../svc-core/PLAN.md §7 — a publish failure never fails the API request
// that triggered it.
type Publisher struct {
	js jetstream.JetStream
}

func NewPublisher(js jetstream.JetStream) *Publisher {
	return &Publisher{js: js}
}

func (p *Publisher) PublishScheduleCreated(ctx context.Context, evt ScheduleEvent) error {
	return p.publish(ctx, SubjectScheduleCreated, evt.ScheduleID, evt.OccurredAt, evt)
}

func (p *Publisher) PublishScheduleUpdated(ctx context.Context, evt ScheduleEvent) error {
	return p.publish(ctx, SubjectScheduleUpdated, evt.ScheduleID, evt.OccurredAt, evt)
}

func (p *Publisher) PublishScheduleDeleted(ctx context.Context, evt ScheduleDeletedEvent) error {
	return p.publish(ctx, SubjectScheduleDeleted, evt.ScheduleID, evt.OccurredAt, evt)
}

// PublishSerialized publishes an already serialized event payload. It is used
// by the transactional outbox after the database transaction has committed.
// Subject, payload, and JetStream deduplication identifiers stay identical to
// the direct publisher API so consumers need no contract change.
func (p *Publisher) PublishSerialized(ctx context.Context, subject, scheduleID string, occurredAt time.Time, data []byte) error {
	return p.publishData(ctx, subject, scheduleID, occurredAt, data, nil)
}

// PublishSerializedWithHeaders restores propagation headers captured when the
// originating HTTP mutation was committed, including on a later retry.
func (p *Publisher) PublishSerializedWithHeaders(ctx context.Context, subject, scheduleID string, occurredAt time.Time, data []byte, headers map[string]string) error {
	return p.publishData(ctx, subject, scheduleID, occurredAt, data, headers)
}

// publish marshals payload, attaches the dedup/tracing/content headers
// mandated by ../../PLAN.md §7.1, and publishes to JetStream. Every attempt
// — success or failure — increments the matching Prometheus counter
// (../../PLAN.md §8.2); failures are also logged at ERROR level per
// ../../svc-core/PLAN.md §7.
func (p *Publisher) publish(ctx context.Context, subject, scheduleID string, occurredAt time.Time, payload any) error {
	if p.js == nil {
		observability.DomainEventPublishFailedTotal.WithLabelValues(subject).Inc()
		err := fmt.Errorf("publish %s: nats not connected", subject)
		log.Printf("ERROR domain event publish failed: subject=%s schedule_id=%s err=%v", subject, scheduleID, err)
		return err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		observability.DomainEventPublishFailedTotal.WithLabelValues(subject).Inc()
		return fmt.Errorf("marshal %s event: %w", subject, err)
	}

	return p.publishData(ctx, subject, scheduleID, occurredAt, data, nil)
}

func (p *Publisher) publishData(ctx context.Context, subject, scheduleID string, occurredAt time.Time, data []byte, headers map[string]string) error {
	if p.js == nil {
		observability.DomainEventPublishFailedTotal.WithLabelValues(subject).Inc()
		err := fmt.Errorf("publish %s: nats not connected", subject)
		log.Printf("ERROR domain event publish failed: subject=%s schedule_id=%s err=%v", subject, scheduleID, err)
		return err
	}

	msg := nats.NewMsg(subject)
	msg.Data = data
	msg.Header.Set("Content-Type", "application/json")
	if len(headers) == 0 {
		otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(msg.Header))
	} else {
		for key, value := range headers {
			msg.Header[key] = []string{value}
		}
	}

	dedupID := fmt.Sprintf("%s:%s:%s", subject, scheduleID, occurredAt.UTC().Format(time.RFC3339Nano))

	if _, err := p.js.PublishMsg(ctx, msg, jetstream.WithMsgID(dedupID)); err != nil {
		observability.DomainEventPublishFailedTotal.WithLabelValues(subject).Inc()
		log.Printf("ERROR domain event publish failed: subject=%s schedule_id=%s err=%v", subject, scheduleID, err)
		return fmt.Errorf("publish %s: %w", subject, err)
	}

	observability.DomainEventPublishedTotal.WithLabelValues(subject).Inc()
	return nil
}
