package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

const maxICSExportEvents = 100

func (h *Handler) ExportSchedulesICS(w http.ResponseWriter, r *http.Request) {
	userID, ok := authmw.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	query := r.URL.Query()
	from, err := time.Parse(time.RFC3339, query.Get("from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "from is required and must be RFC3339")
		return
	}
	to, err := time.Parse(time.RFC3339, query.Get("to"))
	if err != nil || !from.Before(to) {
		writeError(w, http.StatusBadRequest, "to is required, must be RFC3339, and must be after from")
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "date range must not exceed 366 days")
		return
	}
	timezone := query.Get("timezone")
	if timezone == "" {
		timezone = "UTC"
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
		return
	}
	from, to = from.UTC(), to.UTC()
	fromDate := from.In(zone).Format("2006-01-02")
	toLocal := to.In(zone)
	toDate := toLocal.Format("2006-01-02")
	if h, m, s := toLocal.Clock(); h != 0 || m != 0 || s != 0 || toLocal.Nanosecond() != 0 {
		toDate = toLocal.AddDate(0, 0, 1).Format("2006-01-02")
	}
	schedules, hasMore, err := h.svc.ListSchedulesPage(r.Context(), userID, from, to, fromDate, toDate, nil, "", nil, maxICSExportEvents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to export schedules")
		return
	}
	if hasMore {
		writeError(w, http.StatusRequestEntityTooLarge, "export exceeds 100 schedules; narrow the date range")
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="schedules.ics"`)
	w.Header().Set("X-Event-Count", fmt.Sprint(len(schedules)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(schedulesCalendar(schedules)))
}

func schedulesCalendar(schedules []*service.Schedule) string {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//svc-core//Schedule Calendar//EN", "CALSCALE:GREGORIAN"}
	for _, schedule := range schedules {
		lines = append(lines, "BEGIN:VEVENT", "UID:"+schedule.ID.String()+"@svc-core", "DTSTAMP:"+schedule.CreatedAt.UTC().Format("20060102T150405Z"))
		if schedule.AllDay && schedule.StartDate != nil && schedule.EndDate != nil {
			lines = append(lines, "DTSTART;VALUE=DATE:"+strings.ReplaceAll(*schedule.StartDate, "-", ""), "DTEND;VALUE=DATE:"+strings.ReplaceAll(*schedule.EndDate, "-", ""))
		} else {
			lines = append(lines, "DTSTART:"+schedule.StartAt.UTC().Format("20060102T150405Z"))
			if schedule.EndAt != nil {
				lines = append(lines, "DTEND:"+schedule.EndAt.UTC().Format("20060102T150405Z"))
			}
		}
		lines = append(lines, "SUMMARY:"+escapeICS(schedule.Title))
		if schedule.Description != nil {
			lines = append(lines, "DESCRIPTION:"+escapeICS(*schedule.Description))
		}
		if schedule.Location != nil {
			lines = append(lines, "LOCATION:"+escapeICS(*schedule.Location))
		}
		if schedule.Status == "cancelled" {
			lines = append(lines, "STATUS:CANCELLED")
		} else if schedule.Status == "tentative" {
			lines = append(lines, "STATUS:TENTATIVE")
		} else {
			lines = append(lines, "STATUS:CONFIRMED")
		}
		lines = append(lines, "END:VEVENT")
	}
	lines = append(lines, "END:VCALENDAR")
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(foldICSLine(line))
		out.WriteString("\r\n")
	}
	return out.String()
}

func escapeICS(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	value = strings.ReplaceAll(value, "\r\n", `\n`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\n`)
	return value
}

// foldICSLine follows RFC 5545's 75-octet limit without splitting UTF-8 runes.
func foldICSLine(line string) string {
	var out strings.Builder
	column := 0
	for _, r := range line {
		width := len(string(r))
		if column+width > 75 {
			out.WriteString("\r\n ")
			column = 1
		}
		out.WriteRune(r)
		column += width
	}
	return out.String()
}
