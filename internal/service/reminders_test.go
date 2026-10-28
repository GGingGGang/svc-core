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
		{name: "five is allowed", reminders: []ReminderInput{{0, "push"}, {1, "push"}, {2, "email"}, {3, "none"}, {10080, "push"}}},
		{name: "six is rejected", reminders: []ReminderInput{{0, "push"}, {1, "push"}, {2, "push"}, {3, "push"}, {4, "push"}, {5, "push"}}, wantErr: ErrReminderLimit},
		{name: "same offset and channel is rejected", reminders: []ReminderInput{{10, "push"}, {10, "push"}}, wantErr: ErrDuplicateReminder},
		{name: "same offset on another channel is allowed", reminders: []ReminderInput{{10, "push"}, {10, "email"}}},
		{name: "negative offset is rejected", reminders: []ReminderInput{{-1, "push"}}, wantErr: ErrInvalidReminder},
		{name: "over seven days is rejected", reminders: []ReminderInput{{10081, "push"}}, wantErr: ErrInvalidReminder},
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
