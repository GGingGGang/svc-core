-- name: CreateSchedule :exec
INSERT INTO schedules (
  id, user_id, title, description, location, start_at, end_at, all_day, timezone, start_date, end_date, status, source, extraction_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSchedule :one
SELECT * FROM schedules
WHERE id = ? AND user_id = ?;

-- name: ListSchedules :many
SELECT * FROM schedules
WHERE user_id = ?
  AND start_at >= ?
  AND start_at < ?
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
ORDER BY start_at ASC, id ASC;

-- name: ListSchedulesPage :many
SELECT *
FROM schedules
WHERE user_id = ?
  AND ((all_day = 1 AND start_date < ? AND end_date > ?)
    OR (all_day = 0 AND ((end_at IS NULL AND start_at >= ? AND start_at < ?)
      OR (end_at IS NOT NULL AND start_at < ? AND end_at > ?))))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.arg('query') = '' OR LOCATE(sqlc.arg('query'), title) > 0 OR LOCATE(sqlc.arg('query'), COALESCE(location, '')) > 0)
  AND (sqlc.narg('cursor_start_at') IS NULL OR start_at > sqlc.narg('cursor_start_at') OR (start_at = sqlc.narg('cursor_start_at') AND id > sqlc.arg('cursor_id')))
ORDER BY start_at ASC, id ASC
LIMIT ?;

-- name: UpdateSchedule :exec
UPDATE schedules
SET title       = ?,
    description = ?,
    location    = ?,
    start_at    = ?,
    end_at      = ?,
    all_day     = ?,
    timezone    = ?,
    start_date  = ?,
    end_date    = ?,
    status      = ?,
    revision    = revision + 1
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
) VALUES (?, ?, ?, ?, NULL, NULL, ?, ?);
