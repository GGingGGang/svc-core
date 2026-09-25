// Package service holds the schedules domain logic: it enforces user
// scoping (cross-user access is surfaced as ErrNotFound, never a 403, so
// existence is never leaked) and maps between repo (sqlc) rows and domain
// types.
package service

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/GGingGGang/svc-core/internal/ai"
	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/outbox"
	"github.com/GGingGGang/svc-core/internal/repo"
)

// ErrNotFound is returned whenever a schedule/reminder does not exist or
// does not belong to the requesting user.
var ErrNotFound = errors.New("not found")
var ErrIdempotencyConflict = errors.New("idempotency key reused with different content")

type Service struct {
	q      *repo.Queries
	db     *sql.DB
	pub    *events.Publisher
	outbox *outbox.Dispatcher
	aiClnt *ai.Client
}

// New wires the service against db and, if pub is non-nil, publishes a
// schedules.*.v1 event (../../PLAN.md §7) after every commit that creates,
// updates, or deletes a schedule. pub may be nil (e.g. NATS unreachable at
// startup) — publishing is then simply skipped rather than failing the
// request, matching the best-effort contract in ./PLAN.md §7. aiClnt backs
// ExtractSchedules (../../PLAN.md §6) and may be nil in tests that never
// call it.
func New(db *sql.DB, pub *events.Publisher, aiClnt *ai.Client) *Service {
	return &Service{q: repo.New(db), db: db, pub: pub, outbox: outbox.NewDispatcher(db, pub), aiClnt: aiClnt}
}

// RunOutbox continuously retries committed events that could not be sent to
// NATS. It is owned by the process lifecycle, not an HTTP request.
func (s *Service) RunOutbox(ctx context.Context) { s.outbox.Run(ctx) }

// FollowupAvailable means the NATS handoff path has no known delay. It does
// not mean Batch has processed an event or that a reminder was sent.
func (s *Service) FollowupAvailable(ctx context.Context) (bool, error) {
	if s.pub == nil || !s.pub.Connected() {
		return false, nil
	}
	var delayed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM event_outbox
 WHERE published_at IS NULL AND created_at <= DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 5 SECOND))`).Scan(&delayed)
	if err != nil {
		return false, err
	}
	return !delayed, nil
}

func (s *Service) dispatchOutbox(ctx context.Context) {
	if _, err := s.outbox.DispatchOnce(ctx); err != nil {
		// The durable row remains available for the background worker.
		log.Printf("ERROR dispatch schedule event outbox: dispatch_failed")
	}
}
