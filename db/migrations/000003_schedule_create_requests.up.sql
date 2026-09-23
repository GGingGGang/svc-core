CREATE TABLE schedule_create_requests (
  user_id         BINARY(16) NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
  request_hash    BINARY(32) NOT NULL,
  response_json   JSON NULL,
  expires_at      DATETIME(3) NOT NULL,
  PRIMARY KEY (user_id, idempotency_key),
  KEY idx_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
