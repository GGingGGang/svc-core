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

// CreateRecurringSchedules materializes every bounded occurrence as an ordinary
// schedule row so existing event, reminder, revision, and dispatch consumers
// keep their per-schedule contracts.
func (s *Service) CreateRecurringSchedules(ctx context.Context, userID uuid.UUID, in CreateScheduleInput, occurrences []ScheduleOccurrence) (*Schedule, error) {
	if in.Recurrence == nil || len(occurrences) == 0 || len(occurrences) > 100 {
		return nil, ErrInvalidSchedule
	}
	if err := validateReminderSet(in.Reminders); err != nil {
		return nil, err
	}
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
	seriesID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schedule_series (series_id,user_id,recurrence_json) VALUES (?,?,?)", idBytes(seriesID), idBytes(userID), mustJSON(in.Recurrence)); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(occurrences))
	q := s.q.WithTx(tx)
	status := in.Status
	if status == "" {
		status = "confirmed"
	}
	source := in.Source
	if source == "" {
		source = "manual"
	}
	for index, occurrence := range occurrences {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		ids[index] = id
		if err := q.CreateSchedule(ctx, repo.CreateScheduleParams{
			ID: idBytes(id), UserID: idBytes(userID), Title: in.Title,
			Description: nullString(in.Description), Location: nullString(in.Location),
			StartAt: occurrence.StartAt, EndAt: nullTime(occurrence.EndAt), AllDay: occurrence.AllDay,
			Timezone: in.Timezone, StartDate: nullDate(occurrence.StartDate), EndDate: nullDate(occurrence.EndDate),
			Status: repo.SchedulesStatus(status), Source: repo.SchedulesSource(source),
			ExtractionID: extractionIDToNullString(in.ExtractionID),
		}); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schedule_occurrences (schedule_id,series_id,occurrence_index,local_start,local_date) VALUES (?,?,?,?,?)", idBytes(id), idBytes(seriesID), index, occurrence.LocalStart, occurrence.LocalStart.Format("2006-01-02")); err != nil {
			return nil, err
		}
		for _, reminder := range in.Reminders {
			reminderID, err := uuid.NewV7()
			if err != nil {
				return nil, err
			}
			if err := q.AddReminder(ctx, repo.AddReminderParams{ID: idBytes(reminderID), ScheduleID: idBytes(id), MinutesBefore: reminder.MinutesBefore, Channel: repo.ScheduleRemindersChannel(reminder.Channel)}); err != nil {
				return nil, err
			}
		}
	}
	var first *Schedule
	for index, id := range ids {
		schedule, err := getSchedule(ctx, q, userID, id)
		if err != nil {
			return nil, err
		}
		if err := enqueueScheduleEvent(ctx, tx, events.SubjectScheduleCreated, schedule); err != nil {
			return nil, err
		}
		if index == 0 {
			first = schedule
		}
	}
	if in.IdempotencyKey != "" {
		response, err := json.Marshal(first)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_create_requests SET response_json=? WHERE user_id=? AND idempotency_key=?", response, idBytes(userID), in.IdempotencyKey); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	observability.ScheduleMutationsTotal.WithLabelValues("create").Add(float64(len(occurrences)))
	s.dispatchOutbox(ctx)
	return first, nil
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
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

	sch, err := mapSchedule(row, reminders)
	if err != nil {
		return nil, err
	}
	if err := loadOccurrenceMetadata(ctx, q, sch); err != nil {
		return nil, err
	}
	return sch, nil
}

func loadOccurrenceMetadata(ctx context.Context, q *repo.Queries, schedule *Schedule) error {
	metadata, err := q.GetScheduleOccurrence(ctx, repo.GetScheduleOccurrenceParams{ScheduleID: idBytes(schedule.ID), UserID: idBytes(schedule.UserID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	seriesID, err := toUUID(metadata.SeriesID)
	if err != nil {
		return err
	}
	index := int(metadata.OccurrenceIndex)
	schedule.SeriesID = &seriesID
	schedule.OccurrenceIndex = &index
	var rule RecurrenceRule
	if err := json.Unmarshal(metadata.RecurrenceJSON, &rule); err != nil {
		return err
	}
	schedule.Recurrence = &rule
	return nil
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
		if err := loadOccurrenceMetadata(ctx, s.q, sch); err != nil {
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
	if patch.Scope == "following" {
		return s.updateScheduleFollowing(ctx, userID, id, patch, key, requestHash)
	}
	if patch.Scope != "" && patch.Scope != "this" {
		return nil, ErrInvalidSchedule
	}
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

func (s *Service) updateScheduleFollowing(ctx context.Context, userID, id uuid.UUID, patch SchedulePatch, key string, requestHash [32]byte) (*Schedule, error) {
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
	metadata, err := q.GetScheduleOccurrence(ctx, repo.GetScheduleOccurrenceParams{ScheduleID: idBytes(id), UserID: idBytes(userID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotRecurring
	}
	if err != nil {
		return nil, err
	}
	type target struct {
		id       uuid.UUID
		index    int
		revision int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT schedule_id, occurrence_index FROM schedule_occurrences WHERE series_id=? AND occurrence_index >= (SELECT occurrence_index FROM schedule_occurrences WHERE schedule_id=?) ORDER BY occurrence_index`, metadata.SeriesID, idBytes(id))
	if err != nil {
		return nil, err
	}
	targets := make([]target, 0, 100)
	for rows.Next() {
		var raw []byte
		var index int
		if err := rows.Scan(&raw, &index); err != nil {
			rows.Close()
			return nil, err
		}
		targetID, err := toUUID(raw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, target{id: targetID, index: index})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, ErrNotFound
	}
	for i := range targets {
		revision, err := lockScheduleRevision(ctx, tx, userID, targets[i].id)
		if err != nil {
			return nil, err
		}
		targets[i].revision = revision
	}
	var selectedRevision int64
	for _, target := range targets {
		if target.id == id {
			selectedRevision = target.revision
		}
	}
	if patch.ExpectedRevision != nil && selectedRevision != *patch.ExpectedRevision {
		return nil, ErrRevisionConflict
	}
	selectedBefore, err := getSchedule(ctx, q, userID, id)
	if err != nil {
		return nil, err
	}
	selectedFields, err := mergeSchedulePatchFields(selectedBefore, patch)
	if err != nil {
		return nil, err
	}
	if err := validateScheduleFields(selectedFields); err != nil {
		return nil, err
	}
	seriesZone := selectedBefore.Timezone
	if seriesZone == "" {
		seriesZone = "UTC"
	}
	oldStartWall := wallClockService(selectedBefore.StartAt.In(mustLoadLocation(seriesZone)))
	newStartWall := wallClockService(selectedFields.StartAt.In(mustLoadLocation(selectedFields.Timezone)))
	wallShift := newStartWall.Sub(oldStartWall)
	newWallDuration := time.Duration(0)
	if selectedFields.EndAt != nil {
		newWallDuration = wallClockService(selectedFields.EndAt.In(mustLoadLocation(selectedFields.Timezone))).Sub(newStartWall)
	}
	dayShift := 0
	selectedOldDate, selectedNewDate := selectedBefore.StartDate, selectedFields.StartDate
	if selectedOldDate != nil && selectedNewDate != nil {
		oldDate, _ := time.Parse("2006-01-02", *selectedOldDate)
		newDate, _ := time.Parse("2006-01-02", *selectedNewDate)
		dayShift = int(newDate.Sub(oldDate).Hours() / 24)
	}
	var selectedUpdated *Schedule
	for _, item := range targets {
		before, err := getSchedule(ctx, q, userID, item.id)
		if err != nil {
			return nil, err
		}
		fields := selectedFields
		if item.id != id {
			fields, err = mergeSchedulePatchFields(before, patch)
			if err != nil {
				return nil, err
			}
			if fields.AllDay {
				zone, _ := time.LoadLocation(fields.Timezone)
				targetDate := before.StartDate
				if targetDate == nil {
					value := before.StartAt.In(zone).Format("2006-01-02")
					targetDate = &value
				}
				date, parseErr := time.Parse("2006-01-02", *targetDate)
				if parseErr != nil {
					return nil, ErrInvalidSchedule
				}
				date = date.AddDate(0, 0, dayShift)
				startValue := date.Format("2006-01-02")
				span := 1
				if selectedFields.StartDate != nil && selectedFields.EndDate != nil {
					start, _ := time.Parse("2006-01-02", *selectedFields.StartDate)
					end, _ := time.Parse("2006-01-02", *selectedFields.EndDate)
					span = int(end.Sub(start).Hours() / 24)
				}
				endValue := date.AddDate(0, 0, span).Format("2006-01-02")
				fields.StartDate, fields.EndDate = &startValue, &endValue
				fields.StartAt = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, zone).UTC()
				endDate := date.AddDate(0, 0, span)
				endAt := time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 0, 0, 0, 0, zone).UTC()
				fields.EndAt = &endAt
			} else if patch.StartAt != nil || patch.EndAt != nil || patch.Timezone != nil || patch.AllDay != nil {
				zone, zoneErr := time.LoadLocation(fields.Timezone)
				if zoneErr != nil {
					return nil, ErrInvalidSchedule
				}
				targetWall := wallClockService(before.StartAt.In(zone)).Add(wallShift)
				startAt, resolveErr := resolveServiceWall(targetWall, fields.Timezone, patch.StartFold)
				if resolveErr != nil {
					return nil, ErrInvalidSchedule
				}
				fields.StartAt = startAt
				if patch.EndAt != nil && *patch.EndAt == nil {
					fields.EndAt = nil
				} else if selectedFields.EndAt != nil {
					endWall := targetWall.Add(newWallDuration)
					endAt, resolveErr := resolveServiceWall(endWall, fields.Timezone, patch.EndFold)
					if resolveErr != nil || !endAt.After(startAt) {
						return nil, ErrInvalidSchedule
					}
					fields.EndAt = &endAt
				} else {
					fields.EndAt = nil
				}
			}
		}
		if err := validateScheduleFields(fields); err != nil {
			return nil, err
		}
		if err := q.UpdateSchedule(ctx, repo.UpdateScheduleParams{Title: fields.Title, Description: nullString(fields.Description), Location: nullString(fields.Location), StartAt: fields.StartAt, EndAt: nullTime(fields.EndAt), AllDay: fields.AllDay, Timezone: fields.Timezone, StartDate: nullDate(fields.StartDate), EndDate: nullDate(fields.EndDate), Status: repo.SchedulesStatus(fields.Status), ID: idBytes(item.id), UserID: idBytes(userID)}); err != nil {
			return nil, err
		}
		updated, err := getSchedule(ctx, q, userID, item.id)
		if err != nil {
			return nil, err
		}
		if err := enqueueScheduleEvent(ctx, tx, events.SubjectScheduleUpdated, updated); err != nil {
			return nil, err
		}
		if item.id == id {
			selectedUpdated = updated
		}
	}
	if key != "" {
		response, err := json.Marshal(selectedUpdated)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_mutation_requests SET response_json=? WHERE user_id=? AND operation='update' AND idempotency_key=?", response, idBytes(userID), key); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.dispatchOutbox(ctx)
	return selectedUpdated, nil
}

func mergeSchedulePatchFields(before *Schedule, patch SchedulePatch) (ScheduleFields, error) {
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
			return fields, ErrInvalidSchedule
		}
		if fields.StartDate == nil {
			value := fields.StartAt.In(zone).Format("2006-01-02")
			fields.StartDate = &value
		}
		if fields.EndDate == nil {
			start, err := time.Parse("2006-01-02", *fields.StartDate)
			if err != nil {
				return fields, ErrInvalidSchedule
			}
			value := start.AddDate(0, 0, 1).Format("2006-01-02")
			fields.EndDate = &value
		}
		start, startErr := time.Parse("2006-01-02", *fields.StartDate)
		end, endErr := time.Parse("2006-01-02", *fields.EndDate)
		if startErr != nil || endErr != nil || !end.After(start) {
			return fields, ErrInvalidSchedule
		}
		fields.StartAt = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, zone).UTC()
		endAt := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, zone).UTC()
		fields.EndAt = &endAt
	} else {
		fields.StartDate, fields.EndDate = nil, nil
	}
	return fields, nil
}

func validateScheduleFields(fields ScheduleFields) error {
	if fields.EndAt != nil && fields.EndAt.Sub(fields.StartAt) < time.Millisecond {
		return ErrInvalidSchedule
	}
	return nil
}

func wallClockService(value time.Time) time.Time {
	y, m, d := value.Date()
	h, min, sec := value.Clock()
	return time.Date(y, m, d, h, min, sec, value.Nanosecond(), time.UTC)
}

func mustLoadLocation(name string) *time.Location {
	zone, _ := time.LoadLocation(name)
	if zone == nil {
		return time.UTC
	}
	return zone
}

func resolveServiceWall(wall time.Time, timezone, fold string) (time.Time, error) {
	text := wall.Format("2006-01-02T15:04:05")
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	candidateSet := map[int64]time.Time{}
	for _, offset := range []time.Duration{-48 * time.Hour, -24 * time.Hour, -6 * time.Hour, 0, 6 * time.Hour, 24 * time.Hour, 48 * time.Hour} {
		_, seconds := wall.Add(offset).In(zone).Zone()
		candidate := wall.Add(-time.Duration(seconds) * time.Second)
		if candidate.In(zone).Format("2006-01-02T15:04:05") == text {
			candidateSet[candidate.UTC().UnixNano()] = candidate.UTC()
		}
	}
	unique := make([]time.Time, 0, len(candidateSet))
	for _, value := range candidateSet {
		unique = append(unique, value)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].Before(unique[j]) })
	if len(unique) == 0 {
		return time.Time{}, ErrInvalidSchedule
	}
	if len(unique) == 1 {
		return unique[0], nil
	}
	if fold == "earlier" {
		return unique[0], nil
	}
	if fold == "later" {
		return unique[len(unique)-1], nil
	}
	return time.Time{}, ErrInvalidSchedule
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_series WHERE user_id=? AND NOT EXISTS (SELECT 1 FROM schedule_occurrences WHERE series_id=schedule_series.series_id)", idBytes(userID)); err != nil {
		return err
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

func (s *Service) DeleteScheduleFollowing(ctx context.Context, userID, id uuid.UUID, key string, expectedRevision int64, requestHash [32]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureAccountNotDeleted(ctx, tx, userID); err != nil {
		return err
	}
	if key != "" {
		response, replay, err := claimScheduleMutation(ctx, tx, userID, id, "delete-following", key, requestHash)
		if err != nil {
			return err
		}
		if replay {
			if len(response) == 0 || string(response) == "null" {
				return nil
			}
			return nil
		}
	}
	q := s.q.WithTx(tx)
	metadata, err := q.GetScheduleOccurrence(ctx, repo.GetScheduleOccurrenceParams{ScheduleID: idBytes(id), UserID: idBytes(userID)})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotRecurring
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT schedule_id,occurrence_index FROM schedule_occurrences WHERE series_id=? AND occurrence_index >= (SELECT occurrence_index FROM schedule_occurrences WHERE schedule_id=?) ORDER BY occurrence_index`, metadata.SeriesID, idBytes(id))
	if err != nil {
		return err
	}
	type target struct {
		id       uuid.UUID
		revision int64
	}
	targets := make([]target, 0, 100)
	for rows.Next() {
		var raw []byte
		var index int
		if err := rows.Scan(&raw, &index); err != nil {
			rows.Close()
			return err
		}
		targetID, err := toUUID(raw)
		if err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, target{id: targetID})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(targets) == 0 {
		return ErrNotFound
	}
	for i := range targets {
		revision, err := lockScheduleRevision(ctx, tx, userID, targets[i].id)
		if err != nil {
			return err
		}
		targets[i].revision = revision
	}
	var selectedRevision int64
	for _, item := range targets {
		if item.id == id {
			selectedRevision = item.revision
		}
	}
	if selectedRevision != expectedRevision {
		return ErrRevisionConflict
	}
	idList := make([][]byte, len(targets))
	for i, item := range targets {
		idList[i] = idBytes(item.id)
	}
	n, err := q.DeleteSchedulesByIDs(ctx, repo.DeleteSchedulesByIDsParams{UserID: idBytes(userID), Ids: idList})
	if err != nil {
		return err
	}
	occurredAt, err := occurredAtTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, item := range targets {
		if err := enqueueDeletedEvent(ctx, tx, item.id, userID, occurredAt, item.revision+1); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_series WHERE series_id=? AND NOT EXISTS (SELECT 1 FROM schedule_occurrences WHERE series_id=?)", metadata.SeriesID, metadata.SeriesID); err != nil {
		return err
	}
	if key != "" {
		response, _ := json.Marshal(n)
		if _, err := tx.ExecContext(ctx, "UPDATE schedule_mutation_requests SET response_json=? WHERE user_id=? AND operation='delete-following' AND idempotency_key=?", response, idBytes(userID), key); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	observability.ScheduleMutationsTotal.WithLabelValues("delete").Add(float64(n))
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_series WHERE user_id=? AND NOT EXISTS (SELECT 1 FROM schedule_occurrences WHERE series_id=schedule_series.series_id)", idBytes(userID)); err != nil {
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
