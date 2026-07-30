// Package service holds the schedules domain logic: it enforces user
// scoping (cross-user access is surfaced as ErrNotFound, never a 403, so
// existence is never leaked) and maps between repo (sqlc) rows and domain
// types.
package service

import (
	"database/sql"
	"errors"

	"github.com/GGingGGang/svc-core/internal/ai"
	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/repo"
)

// ErrNotFound is returned whenever a schedule/reminder does not exist or
// does not belong to the requesting user.
var ErrNotFound = errors.New("not found")

type Service struct {
	q      *repo.Queries
	db     *sql.DB
	pub    *events.Publisher
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
	return &Service{q: repo.New(db), db: db, pub: pub, aiClnt: aiClnt}
}
