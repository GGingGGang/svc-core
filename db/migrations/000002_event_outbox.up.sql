CREATE TABLE event_outbox (
  id           BINARY(16)   NOT NULL,
  subject      VARCHAR(128) NOT NULL,
  schedule_id  BINARY(16)   NOT NULL,
  occurred_at  DATETIME(3)  NOT NULL,
  payload      JSON         NOT NULL,
  headers      JSON         NOT NULL,
  attempts     INT          NOT NULL DEFAULT 0,
  available_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  locked_until DATETIME(3)  NULL,
  locked_by    BINARY(16)   NULL,
  last_error   VARCHAR(512) NULL,
  published_at DATETIME(3)  NULL,
  created_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_event_outbox_dispatch (published_at, available_at, locked_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
