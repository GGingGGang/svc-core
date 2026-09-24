CREATE TABLE schedule_mutation_requests (
  user_id BINARY(16) NOT NULL,
  operation VARCHAR(16) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  schedule_id BINARY(16) NOT NULL,
  request_hash BINARY(32) NOT NULL,
  response_json JSON NULL,
  expires_at DATETIME(3) NOT NULL,
  PRIMARY KEY (user_id, operation, idempotency_key),
  KEY idx_mutation_expires (expires_at)
) ENGINE=InnoDB;
