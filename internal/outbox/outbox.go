// Package outbox makes schedule writes and their domain events atomic. A row
// is committed with each mutation, then safely retried until JetStream accepts
// the unchanged serialized payload.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/observability"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

type Dispatcher struct {
	db  *sql.DB
	pub *events.Publisher
}

type claimedEvent struct {
	id, lockID          uuid.UUID
	subject, scheduleID string
	occurredAt          time.Time
	payload             []byte
	headers             map[string]string
}

func NewDispatcher(db *sql.DB, pub *events.Publisher) *Dispatcher {
	return &Dispatcher{db: db, pub: pub}
}

// Enqueue records the exact externally visible event within the caller's
// transaction. Calling it before Commit prevents a committed mutation from
// losing its corresponding event.
func Enqueue(ctx context.Context, tx *sql.Tx, subject string, scheduleID uuid.UUID, occurredAt time.Time, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new outbox id: %w", err)
	}
	headers := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, headers)
	if requestID := observability.RequestID(ctx); requestID != "" {
		headers["x-request-id"] = requestID
	}
	headerData, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("marshal outbox headers: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO event_outbox
 (id, subject, schedule_id, occurred_at, payload, headers) VALUES (?, ?, ?, ?, ?, ?)`,
		id[:], subject, scheduleID[:], occurredAt.UTC(), data, headerData)
	if err != nil {
		return fmt.Errorf("insert event outbox: %w", err)
	}
	return nil
}

// DispatchOnce claims and publishes at most one event. A process crash after
// NATS accepts the message can cause a later retry, which JetStream absorbs
// through the existing Nats-Msg-Id contract.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (bool, error) {
	if d == nil || d.pub == nil {
		return false, nil
	}
	evt, err := d.claim(ctx)
	if err != nil || evt == nil {
		return evt != nil, err
	}
	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = d.pub.PublishSerializedWithHeaders(publishCtx, evt.subject, evt.scheduleID, evt.occurredAt, evt.payload, evt.headers)
	cancel()
	if err == nil {
		_, markErr := d.db.ExecContext(ctx, `UPDATE event_outbox SET published_at=UTC_TIMESTAMP(3), locked_until=NULL, locked_by=NULL, last_error=NULL WHERE id=? AND locked_by=?`, evt.id[:], evt.lockID[:])
		return true, markErr
	}
	if markErr := d.reschedule(ctx, evt, err); markErr != nil {
		return true, markErr
	}
	return true, nil
}

func (d *Dispatcher) claim(ctx context.Context) (*claimedEvent, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id, subject, schedule_id, occurred_at, payload, headers FROM event_outbox
 WHERE published_at IS NULL AND available_at <= UTC_TIMESTAMP(3)
 AND (locked_until IS NULL OR locked_until < UTC_TIMESTAMP(3))
 ORDER BY available_at, created_at LIMIT 1 FOR UPDATE SKIP LOCKED`)
	var rawID, rawSchedule []byte
	var evt claimedEvent
	var rawHeaders []byte
	if err := row.Scan(&rawID, &evt.subject, &rawSchedule, &evt.occurredAt, &evt.payload, &rawHeaders); err != nil {
		if err == sql.ErrNoRows {
			return nil, tx.Commit()
		}
		return nil, err
	}
	if err := json.Unmarshal(rawHeaders, &evt.headers); err != nil {
		return nil, fmt.Errorf("decode outbox headers: %w", err)
	}
	if evt.id, err = uuid.FromBytes(rawID); err != nil {
		return nil, err
	}
	scheduleID, err := uuid.FromBytes(rawSchedule)
	if err != nil {
		return nil, err
	}
	evt.scheduleID = scheduleID.String()
	if evt.lockID, err = uuid.NewV7(); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE event_outbox SET locked_by=?, locked_until=DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 30 SECOND) WHERE id=?`, evt.lockID[:], evt.id[:]); err != nil {
		return nil, err
	}
	return &evt, tx.Commit()
}

func (d *Dispatcher) reschedule(ctx context.Context, evt *claimedEvent, publishErr error) error {
	_, err := d.db.ExecContext(ctx, `UPDATE event_outbox SET attempts=attempts+1,
 available_at=DATE_ADD(UTC_TIMESTAMP(3), INTERVAL LEAST(300, POW(2, LEAST(attempts + 1, 8))) SECOND),
 locked_until=NULL, locked_by=NULL, last_error=? WHERE id=? AND locked_by=?`, truncateError(publishErr), evt.id[:], evt.lockID[:])
	return err
}

func truncateError(err error) string {
	s := err.Error()
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// Run keeps draining retryable rows until shutdown. It has no effect on HTTP
// request results; requests may opportunistically call DispatchOnce as well.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for {
			worked, err := d.DispatchOnce(ctx)
			if err != nil {
				log.Printf("ERROR dispatch outbox event: %v", err)
				break
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
