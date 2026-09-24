package api

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeTitle(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		valid bool
	}{
		{"  회의  ", "회의", true},
		{" \t\n ", "", false},
		{strings.Repeat("한", 255), strings.Repeat("한", 255), true},
		{strings.Repeat("한", 256), strings.Repeat("한", 256), false},
	} {
		got, valid := normalizeTitle(tc.input)
		if got != tc.want || valid != tc.valid {
			t.Errorf("normalizeTitle(%q) = (%q, %v), want (%q, %v)", tc.input, got, valid, tc.want, tc.valid)
		}
	}
}

func TestScheduleInputError(t *testing.T) {
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	before, after := start.Add(-time.Second), start.Add(time.Second)
	for _, tc := range []struct {
		name, description, location string
		end                         *time.Time
		want                        string
	}{
		{name: "nullable end"},
		{name: "valid unicode limits", description: strings.Repeat("😀", 10000), location: strings.Repeat("가", 255), end: &after},
		{name: "description over limit", description: strings.Repeat("😀", 10001), want: "description must contain at most 10000 characters"},
		{name: "location over limit", location: strings.Repeat("가", 256), want: "location must contain at most 255 characters"},
		{name: "equal end", end: &start, want: "end_at must be at least 1ms after start_at"},
		{name: "earlier end", end: &before, want: "end_at must be at least 1ms after start_at"},
		{name: "submillisecond gap", end: timePtr(start.Add(time.Microsecond)), want: "end_at must be at least 1ms after start_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scheduleInputError(&tc.description, &tc.location, start, tc.end)
			if got != tc.want {
				t.Fatalf("scheduleInputError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func timePtr(v time.Time) *time.Time { return &v }
