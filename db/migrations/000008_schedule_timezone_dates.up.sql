ALTER TABLE schedules
  ADD COLUMN timezone VARCHAR(64) NOT NULL DEFAULT 'UTC' AFTER all_day,
  ADD COLUMN start_date DATE NULL AFTER timezone,
  ADD COLUMN end_date DATE NULL AFTER start_date;

UPDATE schedules
SET start_date = DATE(start_at),
    end_date = DATE(COALESCE(end_at, DATE_ADD(start_at, INTERVAL 1 DAY)))
WHERE all_day = 1;
