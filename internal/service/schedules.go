package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/GGingGGang/svc-core/internal/events"
	"github.com/GGingGGang/svc-core/internal/observability"
	"github.com/GGingGGang/svc-core/internal/repo"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

func (s *Service) CreateSchedule(ctx context.Context, userID uuid.UUID, in CreateScheduleInput) (*Schedule, error) {
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if err := validateReminderSet(in.Reminders); err != nil {
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
	in.Status, in.Source = status, source

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureAccountNotDeleted(ctx, tx, userID); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if previous, err := claimScheduleCreate(ctx, tx, userID, in); err != nil || previous != nil {
			return previous, err
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

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
		Timezone:     in.Timezone,
		StartDate:    nullDate(in.StartDate),
		EndDate:      nullDate(in.EndDate),
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
	if in.IdempotencyKey != "" {
		payload, err := json.Marshal(sch)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_create_requests SET response_json = ? WHERE user_id = ? AND idempotency_key = ?", payload, idBytes(userID), in.IdempotencyKey); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	observability.ScheduleMutationsTotal.WithLabelValues("create").Inc()
	s.dispatchOutbox(ctx)
	return sch, nil
}

// claimScheduleCreate locks one user's create operation until its schedule,
// reminders, outbox event, and replay response commit together.
func claimScheduleCreate(ctx context.Context, tx *sql.Tx, userID uuid.UUID, in CreateScheduleInput) (*Schedule, error) {
	key := in.IdempotencyKey
	in.IdempotencyKey = ""
	request, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(request)
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_create_requests WHERE user_id = ? AND idempotency_key = ? AND expires_at <= UTC_TIMESTAMP(3)", idBytes(userID), key); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO schedule_create_requests (user_id, idempotency_key, request_hash, expires_at) VALUES (?, ?, ?, DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 24 HOUR))", idBytes(userID), key, hash[:])
	if err == nil {
		return nil, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return nil, err
	}
	var savedHash, response []byte
	if err := tx.QueryRowContext(ctx, "SELECT request_hash, response_json FROM schedule_create_requests WHERE user_id = ? AND idempotency_key = ? FOR UPDATE", idBytes(userID), key).Scan(&savedHash, &response); err != nil {
		return nil, err
	}
	if !bytes.Equal(savedHash, hash[:]) {
		return nil, ErrIdempotencyConflict
	}
	var sch Schedule
	if err := json.Unmarshal(response, &sch); err != nil {
		return nil, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM schedules WHERE id = ? AND user_id = ?", idBytes(sch.ID), idBytes(userID)).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIdempotencyConflict
	} else if err != nil {
		return nil, err
	}
	return &sch, nil
}

func (s *Service) GetSchedule(ctx context.Context, userID, id uuid.UUID) (*Schedule, error) {
	return getSchedule(ctx, s.q, userID, id)
}

// ReplayScheduleUpdate answers a committed retry before PATCH merges omitted
// fields with the current row, which may have changed since the first write.
func (s *Service) ReplayScheduleUpdate(ctx context.Context, userID, id uuid.UUID, key string, requestHash [32]byte) (*Schedule, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	var savedID, savedHash, response []byte
	err := s.db.QueryRowContext(ctx, `SELECT schedule_id, request_hash, response_json FROM schedule_mutation_requests
 WHERE user_id = ? AND operation = 'update' AND idempotency_key = ? AND expires_at > UTC_TIMESTAMP(3)`, idBytes(userID), key).Scan(&savedID, &savedHash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !bytes.Equal(savedID, idBytes(id)) || !bytes.Equal(savedHash, requestHash[:]) {
		return nil, false, ErrIdempotencyConflict
	}
	if _, err := s.GetSchedule(ctx, userID, id); err != nil {
		return nil, false, err
	}
	var saved Schedule
	if err := json.Unmarshal(response, &saved); err != nil {
		return nil, false, err
	}
	return &saved, true, nil
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

func (s *Service) ListSchedulesPage(ctx context.Context, userID uuid.UUID, from, to time.Time, fromDate, toDate string, status *string, query string, cursor *ScheduleCursor, limit int) ([]*Schedule, bool, error) {
	var statusParam repo.NullSchedulesStatus
	if status != nil {
		statusParam = repo.NullSchedulesStatus{SchedulesStatus: repo.SchedulesStatus(*status), Valid: true}
	}
	var cursorStartAt sql.NullTime
	var cursorID []byte
	if cursor != nil {
		cursorStartAt = sql.NullTime{Time: cursor.StartAt, Valid: true}
		cursorID = idBytes(cursor.ID)
	}
	rows, err := s.q.ListSchedulesPage(ctx, repo.ListSchedulesPageParams{
		UserID:        idBytes(userID),
		StartDate:     dateBound(toDate),
		EndDate:       dateBound(fromDate),
		StartAt:       from,
		StartAt_2:     to,
		StartAt_3:     to,
		EndAt:         sql.NullTime{Time: from, Valid: true},
		Status:        statusParam,
		Query:         query,
		CursorStartAt: cursorStartAt,
		CursorID:      cursorID,
		Limit:         int32(limit + 1),
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	schedules := make([]*Schedule, 0, len(rows))
	for _, row := range rows {
		sch, err := mapSchedule(row, nil)
		if err != nil {
			return nil, false, err
		}
		schedules = append(schedules, sch)
	}
	return schedules, hasMore, nil
}

func dateBound(value string) sql.NullTime {
	date, err := time.Parse("2006-01-02", value)
	return sql.NullTime{Time: date, Valid: err == nil}
}

// UpdateSchedule merges the patch after locking the row so concurrent changes
// to different fields cannot overwrite each other.
func (s *Service) UpdateSchedule(ctx context.Context, userID, id uuid.UUID, patch SchedulePatch, key string, requestHash [32]byte) (*Schedule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureAccountNotDeleted(ctx, tx, userID); err != nil {
		return nil, err
	}
	if key != "" {
		response, replay, err := claimScheduleMutation(ctx, tx, userID, id, "update", key, requestHash)
		if err != nil {
			return nil, err
		}
		if replay {
			var saved Schedule
			if err := json.Unmarshal(response, &saved); err != nil {
				return nil, err
			}
			if _, err := lockScheduleRevision(ctx, tx, userID, id); err != nil {
				return nil, err
			}
			return &saved, nil
		}
	}
	q := s.q.WithTx(tx)
	revision, err := lockScheduleRevision(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	if patch.ExpectedRevision != nil && revision != *patch.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	before, err := getSchedule(ctx, q, userID, id)
	if err != nil {
		return nil, err
	}
	fields := ScheduleFields{Title: before.Title, Description: before.Description, Location: before.Location, StartAt: before.StartAt, EndAt: before.EndAt, AllDay: before.AllDay, Timezone: before.Timezone, StartDate: before.StartDate, EndDate: before.EndDate, Status: before.Status}
	if patch.Title != nil {
		fields.Title = *patch.Title
	}
	if patch.Description != nil {
		fields.Description = *patch.Description
	}
	if patch.Location != nil {
		fields.Location = *patch.Location
	}
	if patch.StartAt != nil {
		fields.StartAt = *patch.StartAt
	}
	if patch.EndAt != nil {
		fields.EndAt = *patch.EndAt
	}
	if patch.AllDay != nil {
		fields.AllDay = *patch.AllDay
	}
	if patch.Status != nil {
		fields.Status = *patch.Status
	}
	if patch.Timezone != nil {
		fields.Timezone = *patch.Timezone
	}
	if patch.StartDate != nil {
		fields.StartDate = *patch.StartDate
	}
	if patch.EndDate != nil {
		fields.EndDate = *patch.EndDate
	}
	if fields.Timezone == "" {
		fields.Timezone = "UTC"
	}
	if fields.AllDay {
		zone, err := time.LoadLocation(fields.Timezone)
		if err != nil {
			return nil, ErrInvalidSchedule
		}
		if fields.StartDate == nil {
			value := fields.StartAt.In(zone).Format("2006-01-02")
			fields.StartDate = &value
		}
		if fields.EndDate == nil {
			start, err := time.Parse("2006-01-02", *fields.StartDate)
			if err != nil {
				return nil, ErrInvalidSchedule
			}
			value := start.AddDate(0, 0, 1).Format("2006-01-02")
			fields.EndDate = &value
		}
		start, startErr := time.Parse("2006-01-02", *fields.StartDate)
		end, endErr := time.Parse("2006-01-02", *fields.EndDate)
		if startErr != nil || endErr != nil || !end.After(start) {
			return nil, ErrInvalidSchedule
		}
		fields.StartAt = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, zone).UTC()
		endAt := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, zone).UTC()
		fields.EndAt = &endAt
	} else {
		fields.StartDate, fields.EndDate = nil, nil
	}
	if fields.EndAt != nil && fields.EndAt.Sub(fields.StartAt) < time.Millisecond {
		return nil, ErrInvalidSchedule
	}

	if err := q.UpdateSchedule(ctx, repo.UpdateScheduleParams{
		Title:       fields.Title,
		Description: nullString(fields.Description),
		Location:    nullString(fields.Location),
		StartAt:     fields.StartAt,
		EndAt:       nullTime(fields.EndAt),
		AllDay:      fields.AllDay,
		Timezone:    fields.Timezone,
		StartDate:   nullDate(fields.StartDate),
		EndDate:     nullDate(fields.EndDate),
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
	if key != "" {
		response, err := json.Marshal(sch)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_mutation_requests SET response_json = ? WHERE user_id = ? AND operation = 'update' AND idempotency_key = ?", response, idBytes(userID), key); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if before.Status != "cancelled" && fields.Status == "cancelled" {
		observability.ScheduleMutationsTotal.WithLabelValues("cancel").Inc()
	}
	s.dispatchOutbox(ctx)
	return sch, nil
}

func (s *Service) DeleteSchedule(ctx context.Context, userID, id uuid.UUID, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureAccountNotDeleted(ctx, tx, userID); err != nil {
		return err
	}
	if key != "" {
		replayedHash := sha256.Sum256(idBytes(id))
		_, replay, err := claimScheduleMutation(ctx, tx, userID, id, "delete", key, replayedHash)
		if err != nil || replay {
			return err
		}
	}
	revision, err := lockScheduleRevision(ctx, tx, userID, id)
	if err != nil {
		return err
	}
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
	if err := enqueueDeletedEvent(ctx, tx, id, userID, occurredAt, revision+1); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	observability.ScheduleMutationsTotal.WithLabelValues("delete").Inc()
	s.dispatchOutbox(ctx)
	return nil
}

func claimScheduleMutation(ctx context.Context, tx *sql.Tx, userID, scheduleID uuid.UUID, operation, key string, requestHash [32]byte) ([]byte, bool, error) {
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_mutation_requests WHERE user_id = ? AND operation = ? AND idempotency_key = ? AND expires_at <= UTC_TIMESTAMP(3)", idBytes(userID), operation, key); err != nil {
		return nil, false, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schedule_mutation_requests
 (user_id, operation, idempotency_key, schedule_id, request_hash, expires_at)
 VALUES (?, ?, ?, ?, ?, DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 24 HOUR))`, idBytes(userID), operation, key, idBytes(scheduleID), requestHash[:])
	if err == nil {
		return nil, false, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return nil, false, err
	}
	var savedID, savedHash, response []byte
	if err := tx.QueryRowContext(ctx, `SELECT schedule_id, request_hash, response_json FROM schedule_mutation_requests
 WHERE user_id = ? AND operation = ? AND idempotency_key = ? FOR UPDATE`, idBytes(userID), operation, key).Scan(&savedID, &savedHash, &response); err != nil {
		return nil, false, err
	}
	if !bytes.Equal(savedID, idBytes(scheduleID)) || !bytes.Equal(savedHash, requestHash[:]) {
		return nil, false, ErrIdempotencyConflict
	}
	return response, true, nil
}

// BulkDeleteSchedules is all-or-nothing: every requested ID must be owned by
// the caller and remain present after its row lock is acquired.
func (s *Service) BulkDeleteSchedules(ctx context.Context, userID uuid.UUID, ids []uuid.UUID, key string, requestHash [32]byte) (int64, error) {
	idList := make([][]byte, len(ids))
	for i, id := range ids {
		idList[i] = idBytes(id)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := ensureAccountNotDeleted(ctx, tx, userID); err != nil {
		return 0, err
	}
	if key != "" {
		response, replay, err := claimScheduleMutation(ctx, tx, userID, uuid.Nil, "bulk-delete", key, requestHash)
		if err != nil {
			return 0, err
		}
		if replay {
			var saved int64
			if err := json.Unmarshal(response, &saved); err != nil {
				return 0, err
			}
			return saved, nil
		}
	}
	q := s.q.WithTx(tx)
	owned, err := q.ListScheduleIDsByIDs(ctx, repo.ListScheduleIDsByIDsParams{UserID: idBytes(userID), Ids: idList})
	if err != nil {
		return 0, err
	}
	if len(owned) != len(ids) {
		return 0, ErrNotFound
	}
	sort.Slice(owned, func(i, j int) bool { return bytes.Compare(owned[i], owned[j]) < 0 })
	revisions := make(map[uuid.UUID]int64, len(owned))
	for _, raw := range owned {
		id, err := toUUID(raw)
		if err != nil {
			return 0, err
		}
		revision, err := lockScheduleRevision(ctx, tx, userID, id)
		if err != nil {
			return 0, err
		}
		revisions[id] = revision + 1
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
		revision, ok := revisions[id]
		if !ok {
			return 0, ErrNotFound
		}
		if err := enqueueDeletedEvent(ctx, tx, id, userID, occurredAt, revision); err != nil {
			return 0, err
		}
	}
	if key != "" {
		response, err := json.Marshal(n)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_mutation_requests SET response_json = ? WHERE user_id = ? AND operation = 'bulk-delete' AND idempotency_key = ?", response, idBytes(userID), key); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	observability.ScheduleMutationsTotal.WithLabelValues("delete").Add(float64(n))
	s.dispatchOutbox(ctx)
	return n, nil
}

func lockScheduleRevision(ctx context.Context, tx *sql.Tx, userID, id uuid.UUID) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, "SELECT revision FROM schedules WHERE id = ? AND user_id = ? FOR UPDATE", idBytes(id), idBytes(userID)).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return revision, err
}
