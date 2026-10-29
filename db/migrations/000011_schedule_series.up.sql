CREATE TABLE schedule_series (
  series_id BINARY(16) NOT NULL,
  user_id BINARY(16) NOT NULL,
  recurrence_json JSON NOT NULL,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (series_id),
  KEY idx_series_user (user_id, series_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE schedule_occurrences (
  schedule_id BINARY(16) NOT NULL,
  series_id BINARY(16) NOT NULL,
  occurrence_index INT NOT NULL,
  local_start DATETIME NOT NULL,
  local_date DATE NOT NULL,
  PRIMARY KEY (schedule_id),
  UNIQUE KEY uq_series_occurrence (series_id, occurrence_index),
  KEY idx_series_future (series_id, local_start, schedule_id),
  CONSTRAINT fk_occurrence_schedule FOREIGN KEY (schedule_id)
    REFERENCES schedules (id) ON DELETE CASCADE,
  CONSTRAINT fk_occurrence_series FOREIGN KEY (series_id)
    REFERENCES schedule_series (series_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
