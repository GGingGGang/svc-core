package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/repo"
	"github.com/google/uuid"
)

func (s *Service) CreateSchedule(ctx context.Context, userID uuid.UUID, in CreateScheduleInput) (*Schedule, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	status := in.Status
	if status == "" {
		status = string(repo.SchedulesStatusConfirmed)
	}
	source := in.Source
	if source == "" {
		source = string(repo.SchedulesSourceManual)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	if err := q.CreateSchedule(ctx, repo.CreateScheduleParams{
		ID:           idBytes(id),
		UserID:       idBytes(userID),
		Title:        in.Title,
		Description:  nullString(in.Description),
		Location:     nullString(in.Location),
		StartAt:      in.StartAt,
		EndAt:        nullTime(in.EndAt),
		AllDay:       in.AllDay,
		Status:       repo.SchedulesStatus(status),
		Source:       repo.SchedulesSource(source),
		ExtractionID: extractionIDToNullString(in.ExtractionID),
	}); err != nil {
		return nil, err
	}

	for _, rem := range in.Reminders {
		remID, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		if err := q.AddReminder(ctx, repo.AddReminderParams{
			ID:            idBytes(remID),
			ScheduleID:    idBytes(id),
			MinutesBefore: rem.MinutesBefore,
			Channel:       repo.ScheduleRemindersChannel(rem.Channel),
		}); err != nil {
			return nil, err
		}
	}

	sch, err := getSchedule(ctx, q, userID, id)
	if err != nil {
		return nil, err
	}
	if err := enqueueScheduleEvent(ctx, tx, events.SubjectScheduleCreated, sch); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatchOutbox(ctx)
	return sch, nil
}

func (s *Service) GetSchedule(ctx context.Context, userID, id uuid.UUID) (*Schedule, error) {
	return getSchedule(ctx, s.q, userID, id)
}

func getSchedule(ctx context.Context, q *repo.Queries, userID, id uuid.UUID) (*Schedule, error) {
	row, err := q.GetSchedule(ctx, repo.GetScheduleParams{ID: idBytes(id), UserID: idBytes(userID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	reminders, err := q.ListReminders(ctx, row.ID)
	if err != nil {
		return nil, err
	}

	return mapSchedule(row, reminders)
}

// ListSchedules returns schedules with start_at in [from, to). Reminders are
// intentionally omitted from list rows (kept to the dedicated reminders
// endpoints / single GET) to avoid an N+1 fetch on every listing.
func (s *Service) ListSchedules(ctx context.Context, userID uuid.UUID, from, to time.Time, status *string) ([]*Schedule, error) {
	var statusParam repo.NullSchedulesStatus
	if status != nil {
		statusParam = repo.NullSchedulesStatus{SchedulesStatus: repo.SchedulesStatus(*status), Valid: true}
	}

	rows, err := s.q.ListSchedules(ctx, repo.ListSchedulesParams{
		UserID:    idBytes(userID),
		StartAt:   from,
		StartAt_2: to,
		Status:    statusParam,
	})
	if err != nil {
		return nil, err
	}

	schedules := make([]*Schedule, 0, len(rows))
	for _, row := range rows {
		sch, err := mapSchedule(row, nil)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, sch)
	}
	return schedules, nil
}

// UpdateSchedule confirms ownership (ErrNotFound otherwise) then overwrites
// the mutable columns with fields, which the API layer has already merged
// against the current row.
func (s *Service) UpdateSchedule(ctx context.Context, userID, id uuid.UUID, fields ScheduleFields) (*Schedule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	if _, err := getSchedule(ctx, q, userID, id); err != nil {
		return nil, err
	}

	if err := q.UpdateSchedule(ctx, repo.UpdateScheduleParams{
		Title:       fields.Title,
		Description: nullString(fields.Description),
		Location:    nullString(fields.Location),
		StartAt:     fields.StartAt,
		EndAt:       nullTime(fields.EndAt),
		AllDay:      fields.AllDay,
		Status:      repo.SchedulesStatus(fields.Status),
		ID:          idBytes(id),
		UserID:      idBytes(userID),
	}); err != nil {
		return nil, err
	}
	sch, err := getSchedule(ctx, q, userID, id)
	if err != nil {
		return nil, err
	}
	if err := enqueueScheduleEvent(ctx, tx, events.SubjectScheduleUpdated, sch); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatchOutbox(ctx)
	return sch, nil
}

func (s *Service) DeleteSchedule(ctx context.Context, userID, id uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	n, err := q.DeleteSchedule(ctx, repo.DeleteScheduleParams{ID: idBytes(id), UserID: idBytes(userID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	occurredAt, err := occurredAtTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := enqueueDeletedEvent(ctx, tx, id, userID, occurredAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dispatchOutbox(ctx)
	return nil
}

// BulkDeleteSchedules deletes only the ids owned by userID and reports how
// many rows were actually removed; ids belonging to another user (or
// nonexistent) are silently excluded from the count rather than erroring,
// matching the hard-delete/idempotent nature of the endpoint. A
// schedules.deleted.v1 event is published per id actually removed — the
// caller-supplied id list may include ids that never belonged to this user,
// which must not be reported as deleted.
func (s *Service) BulkDeleteSchedules(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int64, error) {
	idList := make([][]byte, len(ids))
	for i, id := range ids {
		idList[i] = idBytes(id)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	owned, err := q.ListScheduleIDsByIDs(ctx, repo.ListScheduleIDsByIDsParams{UserID: idBytes(userID), Ids: idList})
	if err != nil {
		return 0, err
	}

	n, err := q.DeleteSchedulesByIDs(ctx, repo.DeleteSchedulesByIDsParams{UserID: idBytes(userID), Ids: idList})
	if err != nil {
		return 0, err
	}

	occurredAt, err := occurredAtTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, raw := range owned {
		id, err := toUUID(raw)
		if err != nil {
			continue
		}
		if err := enqueueDeletedEvent(ctx, tx, id, userID, occurredAt); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.dispatchOutbox(ctx)
	return n, nil
}
