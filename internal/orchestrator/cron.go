package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronSpec struct {
	minute, hour, day, month, weekday cronField
}

type cronField struct {
	allowed  map[int]bool
	wildcard bool
}

func parseCron(expression string) (cronSpec, error) {
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return cronSpec{}, fmt.Errorf("cron must contain exactly five fields: minute hour day-of-month month day-of-week")
	}
	ranges := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	fields := make([]cronField, 5)
	for i, part := range parts {
		field, err := parseCronField(part, ranges[i][0], ranges[i][1], i == 4)
		if err != nil {
			return cronSpec{}, fmt.Errorf("invalid cron field %q: %w", part, err)
		}
		fields[i] = field
	}
	return cronSpec{fields[0], fields[1], fields[2], fields[3], fields[4]}, nil
}

func parseCronField(value string, min, max int, sundaySeven bool) (cronField, error) {
	field := cronField{allowed: map[int]bool{}}
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			return cronField{}, fmt.Errorf("empty list element")
		}
		base, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			if strings.Contains(stepText, "/") {
				return cronField{}, fmt.Errorf("invalid step")
			}
			var err error
			step, err = strconv.Atoi(stepText)
			if err != nil || step <= 0 {
				return cronField{}, fmt.Errorf("step must be a positive integer")
			}
		}
		start, end := min, max
		if base != "*" {
			startText, endText, hasRange := strings.Cut(base, "-")
			var err error
			start, err = strconv.Atoi(startText)
			if err != nil {
				return cronField{}, fmt.Errorf("value must be an integer, range, or *")
			}
			end = start
			if hasRange {
				if strings.Contains(endText, "-") {
					return cronField{}, fmt.Errorf("invalid range")
				}
				end, err = strconv.Atoi(endText)
				if err != nil || end < start {
					return cronField{}, fmt.Errorf("range end must be at least its start")
				}
			} else if hasStep {
				return cronField{}, fmt.Errorf("steps require * or a range")
			}
		}
		if start < min || end > max {
			return cronField{}, fmt.Errorf("must be between %d and %d", min, max)
		}
		for n := start; n <= end; n += step {
			canonical := n
			if sundaySeven && n == 7 {
				canonical = 0
			}
			field.allowed[canonical] = true
		}
	}
	field.wildcard = len(field.allowed) == max-min+1
	return field, nil
}

func (c cronSpec) matches(t time.Time) bool {
	dom := c.day.allowed[t.Day()]
	dow := c.weekday.allowed[int(t.Weekday())]
	dayMatches := dom || dow
	if c.day.wildcard {
		dayMatches = dow
	}
	if c.weekday.wildcard {
		dayMatches = dom
	}
	return c.minute.allowed[t.Minute()] && c.hour.allowed[t.Hour()] && c.month.allowed[int(t.Month())] && dayMatches
}

func nextCronOccurrence(expression, timezone string, after time.Time) (time.Time, error) {
	spec, err := parseCron(expression)
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid IANA timezone %q: %w", timezone, err)
	}
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	// A fall-back transition contains the same local wall-clock minute twice.
	// Treat that as one calendar occurrence: if the previous occurrence matched,
	// skip a later instant with the same local date and minute.
	var skipWall string
	if local := after.In(location); spec.matches(local) {
		skipWall = cronWallKey(local)
	}
	for candidate.Before(limit) {
		local := candidate.In(location)
		if spec.matches(local) && cronWallKey(local) != skipWall {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron has no occurrence within five years")
}

func cronWallKey(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}
