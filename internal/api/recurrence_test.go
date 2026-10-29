package api

import (
	"testing"
	"time"

	"github.com/GGingGGang/svc-core/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMonthlyRecurrenceSkipsMissingDayAndReportsIt(t *testing.T) {
	zone, err := time.LoadLocation("UTC")
	require.NoError(t, err)
	start := time.Date(2026, time.January, 31, 9, 0, 0, 0, zone)
	count := 3
	occurrences, skipped, err := generateOccurrences(start, nil, false, nil, nil, "UTC", service.RecurrenceRule{Frequency: "MONTHLY", Interval: 1, Count: &count}, "", "")
	require.NoError(t, err)
	require.Equal(t, []string{"2026-01-31", "2026-03-31", "2026-05-31"}, []string{occurrences[0].StartAt.Format("2006-01-02"), occurrences[1].StartAt.Format("2006-01-02"), occurrences[2].StartAt.Format("2006-01-02")})
	require.Equal(t, recurrenceSkipped{LocalDate: "2026-02-28", Reason: "day_of_month_missing"}, skipped[0])
	require.Equal(t, recurrenceSkipped{LocalDate: "2026-04-30", Reason: "day_of_month_missing"}, skipped[1])
}

func TestRecurrenceRejectsNonexistentDSTWallTime(t *testing.T) {
	start, err := resolveLocalDateTime("2026-03-07T02:30:00", "America/New_York", "")
	require.NoError(t, err)
	count := 3
	_, _, err = generateOccurrences(start, nil, false, nil, nil, "America/New_York", service.RecurrenceRule{Frequency: "DAILY", Interval: 1, Count: &count}, "", "")
	require.ErrorIs(t, err, errNonexistentLocalTime)
}

func TestRecurrenceFoldRequiresExplicitChoice(t *testing.T) {
	start := time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC)
	count := 2
	rule := service.RecurrenceRule{Frequency: "DAILY", Interval: 1, Count: &count}
	_, _, err := generateOccurrences(start, nil, false, nil, nil, "America/New_York", rule, "", "")
	require.ErrorIs(t, err, errAmbiguousLocalTime)
	occurrences, _, err := generateOccurrences(start, nil, false, nil, nil, "America/New_York", rule, "earlier", "")
	require.NoError(t, err)
	require.Equal(t, "2026-11-01T05:30:00Z", occurrences[0].StartAt.Format(time.RFC3339))
}

func TestOvernightRecurrencePreservesEndClockAcrossDST(t *testing.T) {
	start, err := resolveLocalDateTime("2026-03-07T23:00:00", "America/New_York", "")
	require.NoError(t, err)
	end, err := resolveLocalDateTime("2026-03-08T01:00:00", "America/New_York", "")
	require.NoError(t, err)
	count := 2
	occurrences, _, err := generateOccurrences(start, &end, false, nil, nil, "America/New_York", service.RecurrenceRule{Frequency: "DAILY", Interval: 1, Count: &count}, "", "")
	require.NoError(t, err)
	require.Len(t, occurrences, 2)
	zone, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	require.Equal(t, "2026-03-08 01:00", occurrences[0].EndAt.In(zone).Format("2006-01-02 15:04"))
	require.Equal(t, "2026-03-09 01:00", occurrences[1].EndAt.In(zone).Format("2006-01-02 15:04"))
}

func TestAllDayRecurrencePreservesExclusiveDateSpanAndExdate(t *testing.T) {
	startDate, endDate := "2026-10-31", "2026-11-02"
	start, err := dateAtTimezoneMidnight(startDate, "America/Los_Angeles")
	require.NoError(t, err)
	count := 2
	rule := service.RecurrenceRule{Frequency: "DAILY", Interval: 1, Count: &count, Exdates: []string{"2026-11-01"}}
	occurrences, _, err := generateOccurrences(start, nil, true, &startDate, &endDate, "America/Los_Angeles", rule, "", "")
	require.NoError(t, err)
	require.Equal(t, []string{"2026-10-31", "2026-11-02"}, []string{*occurrences[0].StartDate, *occurrences[1].StartDate})
	require.Equal(t, []string{"2026-11-02", "2026-11-04"}, []string{*occurrences[0].EndDate, *occurrences[1].EndDate})
}
