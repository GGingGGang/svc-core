ALTER TABLE schedule_reminders
  MODIFY COLUMN channel ENUM('push','email','none','telegram') NOT NULL DEFAULT 'push';
