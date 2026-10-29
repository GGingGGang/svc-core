package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

type recurrenceOccurrence struct {
	Index      int        `json:"index"`
	StartAt    time.Time  `json:"start_at"`
	EndAt      *time.Time `json:"end_at"`
	AllDay     bool       `json:"all_day"`
	StartDate  *string    `json:"start_date"`
	EndDate    *string    `json:"end_date"`
	LocalStart time.Time  `json:"-"`
}

type recurrenceSkipped struct {
	LocalDate string `json:"local_date"`
	Reason    string `json:"reason"`
}

func generateOccurrences(startAt time.Time, endAt *time.Time, allDay bool, startDate, endDate *string, timezone string, rule service.RecurrenceRule, startFold, endFold string) ([]recurrenceOccurrence, []recurrenceSkipped, error) {
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, errors.New("timezone must be an IANA timezone")
	}
	if rule.Frequency != "DAILY" && rule.Frequency != "WEEKLY" && rule.Frequency != "MONTHLY" || rule.Interval < 1 || rule.Interval > 30 {
		return nil, nil, errors.New("frequency or interval is invalid")
	}
	if (rule.Count == nil) == (rule.Until == nil) {
		return nil, nil, errors.New("exactly one of count or until is required")
	}
	if rule.Count != nil && (*rule.Count < 1 || *rule.Count > 100) {
		return nil, nil, errors.New("count must be between 1 and 100")
	}
	if len(rule.Exdates) > 100 {
		return nil, nil, errors.New("exdates must contain at most 100 dates")
	}
	weekdaySet := map[time.Weekday]bool{}
	for _, weekday := range rule.Weekdays {
		value, ok := map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}[weekday]
		if !ok || weekdaySet[value] {
			return nil, nil, errors.New("weekdays must be unique MO..SU codes")
		}
		weekdaySet[value] = true
	}
	if rule.Frequency != "WEEKLY" && len(weekdaySet) > 0 {
		return nil, nil, errors.New("weekdays are only valid for weekly recurrence")
	}
	if rule.Frequency == "WEEKLY" && len(weekdaySet) == 0 {
		weekdaySet[startAt.In(zone).Weekday()] = true
	}
	startLocal := startAt.In(zone)
	anchorDate := startLocal
	if allDay {
		if startDate == nil || endDate == nil {
			return nil, nil, errors.New("all-day recurrence requires start_date and end_date")
		}
		parsed, parseErr := time.Parse("2006-01-02", *startDate)
		if parseErr != nil {
			return nil, nil, errors.New("invalid start_date")
		}
		anchorDate = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, zone)
	}
	anchorDate = time.Date(anchorDate.Year(), anchorDate.Month(), anchorDate.Day(), 0, 0, 0, 0, zone)
	var until time.Time
	if rule.Until != nil {
		until, err = time.Parse("2006-01-02", *rule.Until)
		if err != nil {
			return nil, nil, errors.New("until must be an ISO local date")
		}
		until = time.Date(until.Year(), until.Month(), until.Day(), 0, 0, 0, 0, zone)
		if until.Before(anchorDate) || until.After(anchorDate.AddDate(1, 0, 0)) {
			return nil, nil, errors.New("until must be within one year of the series start")
		}
	}
	exdateSet := map[string]bool{}
	for _, value := range rule.Exdates {
		date, parseErr := time.Parse("2006-01-02", value)
		if parseErr != nil || date.Format("2006-01-02") != value {
			return nil, nil, errors.New("exdates must be ISO local dates")
		}
		exdateSet[value] = true
	}
	var allDaySpan int
	if allDay {
		start, _ := time.Parse("2006-01-02", *startDate)
		end, endErr := time.Parse("2006-01-02", *endDate)
		if endErr != nil || !end.After(start) || end.Sub(start) > 366*24*time.Hour {
			return nil, nil, errors.New("invalid all-day date range")
		}
		allDaySpan = int(end.Sub(start).Hours() / 24)
	}
	startWall := wallClock(startLocal)
	var endWall time.Time
	if endAt != nil {
		endWall = wallClock(endAt.In(zone))
	}
	wallDuration := endWall.Sub(startWall)
	if endAt != nil && wallDuration < time.Millisecond {
		return nil, nil, errors.New("end_at must follow start_at in local time")
	}
	startClock := fmt.Sprintf("%02d:%02d:%02d", startLocal.Hour(), startLocal.Minute(), startLocal.Second())
	startClockDuration := time.Duration(startLocal.Hour())*time.Hour + time.Duration(startLocal.Minute())*time.Minute + time.Duration(startLocal.Second())*time.Second
	occurrences := make([]recurrenceOccurrence, 0, 16)
	skipped := make([]recurrenceSkipped, 0)
	candidates := make([]time.Time, 0, 100)
	maxDate := anchorDate.AddDate(1, 0, 0)
	switch rule.Frequency {
	case "DAILY", "WEEKLY":
		for offset := 0; offset <= 366; offset++ {
			candidate := anchorDate.AddDate(0, 0, offset)
			if candidate.After(maxDate) || rule.Until != nil && candidate.After(until) {
				break
			}
			if recurrenceMatches(anchorDate, candidate, rule, weekdaySet) {
				candidates = append(candidates, candidate)
			}
		}
	case "MONTHLY":
		for offset := 0; offset <= 12; offset += rule.Interval {
			monthStart := time.Date(anchorDate.Year(), anchorDate.Month()+time.Month(offset), 1, 0, 0, 0, 0, zone)
			if monthStart.After(maxDate) || rule.Until != nil && monthStart.After(until) {
				break
			}
			lastDay := time.Date(monthStart.Year(), monthStart.Month()+1, 0, 0, 0, 0, 0, zone).Day()
			if anchorDate.Day() > lastDay {
				missing := time.Date(monthStart.Year(), monthStart.Month(), lastDay, 0, 0, 0, 0, zone)
				skipped = append(skipped, recurrenceSkipped{LocalDate: missing.Format("2006-01-02"), Reason: "day_of_month_missing"})
				continue
			}
			candidate := time.Date(monthStart.Year(), monthStart.Month(), anchorDate.Day(), 0, 0, 0, 0, zone)
			if !candidate.Before(anchorDate) && (rule.Until == nil || !candidate.After(until)) {
				candidates = append(candidates, candidate)
			}
		}
	}
	for _, candidate := range candidates {
		date := candidate.Format("2006-01-02")
		if exdateSet[date] {
			continue
		}
		if allDay {
			endDateValue := candidate.AddDate(0, 0, allDaySpan).Format("2006-01-02")
			startValue := date
			startInstant, resolveErr := dateAtTimezoneMidnight(startValue, timezone)
			endInstant, endErr := dateAtTimezoneMidnight(endDateValue, timezone)
			if resolveErr != nil || endErr != nil {
				return nil, nil, errors.New("invalid all-day occurrence boundary")
			}
			occurrences = append(occurrences, recurrenceOccurrence{Index: len(occurrences), StartAt: startInstant, EndAt: &endInstant, AllDay: true, StartDate: &startValue, EndDate: &endDateValue, LocalStart: wallClock(candidate)})
		} else {
			localStart := date + "T" + startClock
			resolvedStart, resolveErr := resolveRecurringLocal(localStart, timezone, startFold)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("occurrence %s start: %w", date, resolveErr)
			}
			localStartWall := wallClock(candidate).Add(startClockDuration)
			occ := recurrenceOccurrence{Index: len(occurrences), StartAt: resolvedStart, LocalStart: localStartWall}
			if endAt != nil {
				localEnd := localStartWall.Add(wallDuration).Format("2006-01-02T15:04:05")
				resolvedEnd, endResolveErr := resolveRecurringLocal(localEnd, timezone, endFold)
				if endResolveErr != nil {
					return nil, nil, fmt.Errorf("occurrence %s end: %w", date, endResolveErr)
				}
				if !resolvedEnd.After(resolvedStart) {
					return nil, nil, errors.New("recurrence end must follow start")
				}
				occ.EndAt = &resolvedEnd
			}
			occurrences = append(occurrences, occ)
		}
		if rule.Count != nil && len(occurrences) >= *rule.Count {
			break
		}
	}
	if len(occurrences) == 0 || len(occurrences) > 100 {
		return nil, skipped, errors.New("recurrence must produce between 1 and 100 occurrences; set count or shorten until")
	}
	lastDate := occurrences[len(occurrences)-1].LocalStart.Format("2006-01-02")
	if allDay && occurrences[len(occurrences)-1].StartDate != nil {
		lastDate = *occurrences[len(occurrences)-1].StartDate
	}
	filteredSkipped := skipped[:0]
	for _, item := range skipped {
		if item.LocalDate <= lastDate {
			filteredSkipped = append(filteredSkipped, item)
		}
	}
	skipped = filteredSkipped
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].LocalDate < skipped[j].LocalDate })
	return occurrences, skipped, nil
}

func recurrenceMatches(anchor, candidate time.Time, rule service.RecurrenceRule, weekdays map[time.Weekday]bool) bool {
	switch rule.Frequency {
	case "DAILY":
		return dayDelta(anchor, candidate)%rule.Interval == 0
	case "WEEKLY":
		weekStart := anchor.AddDate(0, 0, -((int(anchor.Weekday()) + 6) % 7))
		candidateWeek := candidate.AddDate(0, 0, -((int(candidate.Weekday()) + 6) % 7))
		return dayDelta(weekStart, candidateWeek)/7%rule.Interval == 0 && weekdays[candidate.Weekday()]
	case "MONTHLY":
		return monthDelta(anchor, candidate)%rule.Interval == 0
	default:
		return false
	}
}

func dayDelta(a, b time.Time) int {
	ua := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	ub := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

func monthDelta(a, b time.Time) int { return (b.Year()-a.Year())*12 + int(b.Month()-a.Month()) }

func wallClock(value time.Time) time.Time {
	y, month, day := value.Date()
	hour, minute, second := value.Clock()
	return time.Date(y, month, day, hour, minute, second, value.Nanosecond(), time.UTC)
}

func resolveRecurringLocal(value, timezone, fold string) (time.Time, error) {
	resolved, err := resolveLocalDateTime(value, timezone, "")
	if err == nil || !errors.Is(err, errAmbiguousLocalTime) {
		return resolved, err
	}
	return resolveLocalDateTime(value, timezone, fold)
}

func resolveRecurrenceAnchorTimes(req createScheduleRequest, timezone string) (time.Time, *time.Time, *string, *string, error) {
	start, end, startDate, endDate, err := resolveScheduleTimes(req.StartAt, req.EndAt, req.LocalStartAt, req.LocalEndAt, req.StartDate, req.EndDate, req.AllDay, timezone, "", "")
	if errors.Is(err, errAmbiguousLocalTime) && (req.StartFold != "" || req.EndFold != "") {
		return resolveScheduleTimes(req.StartAt, req.EndAt, req.LocalStartAt, req.LocalEndAt, req.StartDate, req.EndDate, req.AllDay, timezone, req.StartFold, req.EndFold)
	}
	return start, end, startDate, endDate, err
}

func (h *Handler) PreviewRecurrence(w http.ResponseWriter, r *http.Request) {
	if _, ok := authmw.UserID(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Recurrence == nil {
		writeError(w, http.StatusBadRequest, "schedule fields and recurrence are required")
		return
	}
	if _, valid := normalizeTitle(req.Title); !valid {
		writeError(w, http.StatusBadRequest, "title must contain 1 to 255 characters")
		return
	}
	timezone := req.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeError(w, http.StatusBadRequest, "timezone must be an IANA timezone")
		return
	}
	startAt, endAt, startDate, endDate, err := resolveRecurrenceAnchorTimes(req, timezone)
	if err != nil {
		writeTimeInputError(w, err)
		return
	}
	occurrences, skipped, err := generateOccurrences(startAt, endAt, req.AllDay, startDate, endDate, timezone, *req.Recurrence, req.StartFold, req.EndFold)
	if err != nil {
		if errors.Is(err, errNonexistentLocalTime) || errors.Is(err, errAmbiguousLocalTime) {
			writeTimeInputError(w, err)
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"occurrences": occurrences, "skipped": skipped})
}
