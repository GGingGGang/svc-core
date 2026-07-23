-- name: CreateSchedule :exec
INSERT INTO schedules (
  id, user_id, title, description, location, start_at, end_at, all_day, status, source, extraction_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSchedule :one
SELECT * FROM schedules
WHERE id = ? AND user_id = ?;

-- name: ListSchedules :many
SELECT * FROM schedules
WHERE user_id = ?
  AND start_at >= ?
  AND start_at < ?
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
ORDER BY start_at ASC;

-- name: UpdateSchedule :exec
UPDATE schedules
SET title       = ?,
    description = ?,
    location    = ?,
    start_at    = ?,
    end_at      = ?,
    all_day     = ?,
    status      = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteSchedule :execrows
DELETE FROM schedules
WHERE id = ? AND user_id = ?;

-- name: DeleteSchedulesByIDs :execrows
DELETE FROM schedules
WHERE user_id = ? AND id IN (sqlc.slice('ids'));

-- name: ListScheduleIDsByIDs :many
SELECT id FROM schedules
WHERE user_id = ? AND id IN (sqlc.slice('ids'));

-- name: AddReminder :exec
INSERT INTO schedule_reminders (id, schedule_id, minutes_before, channel)
VALUES (?, ?, ?, ?);

-- name: ListReminders :many
SELECT * FROM schedule_reminders
WHERE schedule_id = ?
ORDER BY minutes_before ASC;

-- name: DeleteReminder :execrows
DELETE FROM schedule_reminders
WHERE id = ? AND schedule_id = ?;

-- name: CreateExtraction :exec
INSERT INTO ai_extractions (
  id, user_id, model, input_chars, raw_text, result_json, status, latency_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
