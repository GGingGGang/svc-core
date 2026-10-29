package api

import (
	"strings"
	"testing"
	"time"

	"github.com/GGingGGang/svc-core/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestICSCalendarEscapesTextAndUsesExclusiveAllDayEnd(t *testing.T) {
	start, end := "2026-03-08", "2026-03-10"
	description := "line one\nline,two;slash\\"
	calendar := schedulesCalendar([]*service.Schedule{{
		ID:    uuid.MustParse("018f0000-0000-7000-8000-000000000001"),
		Title: "회의, 일정; \"중요\"", Description: &description,
		AllDay: true, StartDate: &start, EndDate: &end,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Status: "confirmed",
	}})
	require.Contains(t, calendar, "DTSTART;VALUE=DATE:20260308\r\n")
	require.Contains(t, calendar, "DTEND;VALUE=DATE:20260310\r\n")
	require.Contains(t, calendar, `SUMMARY:회의\, 일정\; "중요"`)
	require.Contains(t, calendar, `DESCRIPTION:line one\nline\,two\;slash\\`)
	require.True(t, strings.HasSuffix(calendar, "END:VCALENDAR\r\n"))
}

func TestFoldICSLineRespects75OctetsAndPreservesUTF8(t *testing.T) {
	line := "SUMMARY:" + strings.Repeat("가", 35)
	folded := foldICSLine(line)
	require.True(t, strings.Contains(folded, "\r\n "))
	for _, physicalLine := range strings.Split(folded, "\r\n") {
		require.LessOrEqual(t, len([]byte(physicalLine)), 75)
	}
}
