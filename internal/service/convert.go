package service

import (
	"database/sql"
	"time"

	"github.com/GGingGGang/svc-core/internal/repo"
	"github.com/google/uuid"
)

func idBytes(id uuid.UUID) []byte {
	b := id
	return b[:]
}

func toUUID(b []byte) (uuid.UUID, error) {
	return uuid.FromBytes(b)
}

func nullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func timePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	v := nt.Time.UTC()
	return &v
}

// extraction_id is BINARY(16) NULL. sqlc's MySQL codegen represents nullable
// binary columns as sql.NullString rather than []byte (non-nullable BINARY
// columns map to []byte directly) — the raw 16 bytes ride in the String
// field either way, so conversion just needs to skip UTF-8 assumptions.
func extractionIDToNullString(id *uuid.UUID) sql.NullString {
	if id == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(idBytes(*id)), Valid: true}
}

func nullStringToExtractionID(ns sql.NullString) (*uuid.UUID, error) {
	if !ns.Valid {
		return nil, nil
	}
	id, err := uuid.FromBytes([]byte(ns.String))
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func mapSchedule(row *repo.Schedule, reminders []*repo.ScheduleReminder) (*Schedule, error) {
	id, err := toUUID(row.ID)
	if err != nil {
		return nil, err
	}
	userID, err := toUUID(row.UserID)
	if err != nil {
		return nil, err
	}
	extractionID, err := nullStringToExtractionID(row.ExtractionID)
	if err != nil {
		return nil, err
	}

	sch := &Schedule{
		ID:           id,
		UserID:       userID,
		Title:        row.Title,
		Description:  strPtr(row.Description),
		Location:     strPtr(row.Location),
		StartAt:      row.StartAt.UTC(),
		EndAt:        timePtr(row.EndAt),
		AllDay:       row.AllDay,
		Status:       string(row.Status),
		Source:       string(row.Source),
		ExtractionID: extractionID,
		CreatedAt:    row.CreatedAt.UTC(),
		UpdatedAt:    row.UpdatedAt.UTC(),
		Revision:     row.Revision,
	}
	for _, r := range reminders {
		rem, err := mapReminder(r)
		if err != nil {
			return nil, err
		}
		sch.Reminders = append(sch.Reminders, *rem)
	}
	return sch, nil
}

func mapReminders(rows []*repo.ScheduleReminder) ([]Reminder, error) {
	out := make([]Reminder, 0, len(rows))
	for _, row := range rows {
		rem, err := mapReminder(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *rem)
	}
	return out, nil
}

func mapReminder(row *repo.ScheduleReminder) (*Reminder, error) {
	id, err := toUUID(row.ID)
	if err != nil {
		return nil, err
	}
	scheduleID, err := toUUID(row.ScheduleID)
	if err != nil {
		return nil, err
	}
	return &Reminder{
		ID:            id,
		ScheduleID:    scheduleID,
		MinutesBefore: row.MinutesBefore,
		Channel:       string(row.Channel),
		CreatedAt:     row.CreatedAt.UTC(),
	}, nil
}
