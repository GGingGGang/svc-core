package api

import (
	"errors"
	"sort"
	"time"
)

var (
	errInvalidTimeFields    = errors.New("invalid schedule time fields")
	errNonexistentLocalTime = errors.New("local time does not exist in timezone")
	errAmbiguousLocalTime   = errors.New("local time is ambiguous; choose a fold")
)

func resolveLocalDateTime(value, timezone, fold string) (time.Time, error) {
	wall, err := time.Parse("2006-01-02T15:04:05", value)
	if err != nil {
		return time.Time{}, err
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	y, month, day := wall.Date()
	hour, minute, second := wall.Clock()
	wall = time.Date(y, month, day, hour, minute, second, 0, time.UTC)

	offsets := map[int]struct{}{}
	for _, delta := range []time.Duration{-48 * time.Hour, -24 * time.Hour, -6 * time.Hour, 0, 6 * time.Hour, 24 * time.Hour, 48 * time.Hour} {
		_, offset := wall.Add(delta).In(zone).Zone()
		offsets[offset] = struct{}{}
	}
	candidates := make([]time.Time, 0, 2)
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(zone)
		ly, lm, ld := local.Date()
		lh, lmin, ls := local.Clock()
		if ly == y && lm == month && ld == day && lh == hour && lmin == minute && ls == second {
			candidates = append(candidates, candidate.UTC())
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	switch len(candidates) {
	case 0:
		return time.Time{}, errNonexistentLocalTime
	case 1:
		if fold != "" {
			return time.Time{}, errors.New("fold is only valid for an ambiguous local time")
		}
		return candidates[0], nil
	default:
		switch fold {
		case "earlier":
			return candidates[0], nil
		case "later":
			return candidates[len(candidates)-1], nil
		default:
			return time.Time{}, errAmbiguousLocalTime
		}
	}
}

func dateAtTimezoneMidnight(value, timezone string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	y, month, day := date.Date()
	return time.Date(y, month, day, 0, 0, 0, 0, zone).UTC(), nil
}
