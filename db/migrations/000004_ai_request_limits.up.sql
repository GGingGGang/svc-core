CREATE TABLE ai_request_users (
  user_id BINARY(16) NOT NULL PRIMARY KEY
) ENGINE=InnoDB;

CREATE TABLE ai_request_admissions (
  id BINARY(16) NOT NULL PRIMARY KEY,
  user_id BINARY(16) NOT NULL,
  started_at DATETIME(3) NOT NULL,
  finished_at DATETIME(3) NULL,
  KEY idx_ai_request_user_started (user_id, started_at)
) ENGINE=InnoDB;
