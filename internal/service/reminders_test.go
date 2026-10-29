package service

import (
	"errors"
	"testing"
)

func TestValidateReminderSet(t *testing.T) {
	tests := []struct {
		name      string
		reminders []ReminderInput
		wantErr   error
	}{
		{name: "empty is allowed"},
		{name: "five is allowed", reminders: []ReminderInput{{0, "telegram"}, {1, "telegram"}, {2, "telegram"}, {3, "telegram"}, {10080, "telegram"}}},
		{name: "six is rejected", reminders: []ReminderInput{{0, "telegram"}, {1, "telegram"}, {2, "telegram"}, {3, "telegram"}, {4, "telegram"}, {5, "telegram"}}, wantErr: ErrReminderLimit},
		{name: "same offset and channel is rejected", reminders: []ReminderInput{{10, "telegram"}, {10, "telegram"}}, wantErr: ErrDuplicateReminder},
		{name: "unsupported channel is rejected", reminders: []ReminderInput{{10, "email"}}, wantErr: ErrInvalidReminder},
		{name: "negative offset is rejected", reminders: []ReminderInput{{-1, "telegram"}}, wantErr: ErrInvalidReminder},
		{name: "over seven days is rejected", reminders: []ReminderInput{{10081, "telegram"}}, wantErr: ErrInvalidReminder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReminderSet(tt.reminders)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateReminderSet() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
