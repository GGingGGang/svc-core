-- core schema. user_id = JWT sub (BINARY(16) UUIDv7). cross-schema FK 없음. 모든 시각 UTC.

CREATE TABLE schedules (
  id            BINARY(16)   NOT NULL,
  user_id       BINARY(16)   NOT NULL,
  title         VARCHAR(255) NOT NULL,
  description   TEXT         NULL,
  location      VARCHAR(255) NULL,
  start_at      DATETIME(3)  NOT NULL,
  end_at        DATETIME(3)  NULL,
  all_day       TINYINT(1)   NOT NULL DEFAULT 0,
  status        ENUM('confirmed','tentative','cancelled') NOT NULL DEFAULT 'confirmed',
  source        ENUM('manual','ai')                       NOT NULL DEFAULT 'manual',
  extraction_id BINARY(16)   NULL,
  created_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_user_start (user_id, start_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE schedule_reminders (
  id             BINARY(16) NOT NULL,
  schedule_id    BINARY(16) NOT NULL,
  minutes_before INT        NOT NULL,
  channel        ENUM('push','email','none') NOT NULL DEFAULT 'push',
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_schedule (schedule_id),
  CONSTRAINT fk_reminder_schedule FOREIGN KEY (schedule_id)
    REFERENCES schedules (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 추출 요청 audit (재시도 / 품질 분석). raw_text 는 저장만 — 로그 / metric label 금지(PII).
CREATE TABLE ai_extractions (
  id          BINARY(16)  NOT NULL,
  user_id     BINARY(16)  NOT NULL,
  model       VARCHAR(64) NOT NULL,
  input_chars INT         NOT NULL,
  raw_text    MEDIUMTEXT  NULL,
  result_json JSON        NULL,
  status      ENUM('success','partial','failed') NOT NULL,
  latency_ms  INT         NULL,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_user_created (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
