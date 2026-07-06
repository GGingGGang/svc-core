// Package service holds the schedules domain logic: it enforces user
// scoping (cross-user access is surfaced as ErrNotFound, never a 403, so
// existence is never leaked) and maps between repo (sqlc) rows and domain
// types.
package service

import (
	"database/sql"
	"errors"

	"github.com/GGingGGang/svc-core/internal/repo"
)

// ErrNotFound is returned whenever a schedule/reminder does not exist or
// does not belong to the requesting user.
var ErrNotFound = errors.New("not found")

type Service struct {
	q  *repo.Queries
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{q: repo.New(db), db: db}
}
