package mods

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cron is a five-field schedule: minute hour day-of-month month day-of-week.
type cron [5]map[int]bool

var cronRanges = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

// parseCron accepts *, numbers, ranges (a-b), steps (*/n, a-b/n), and lists (a,b).
func parseCron(spec string) (cron, error) {
	var c cron
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return c, fmt.Errorf("schedule %q needs five fields: minute hour day month weekday", spec)
	}
	for i, f := range fields {
		c[i] = map[int]bool{}
		lo, hi := cronRanges[i][0], cronRanges[i][1]
		for _, part := range strings.Split(f, ",") {
			step := 1
			if base, s, ok := strings.Cut(part, "/"); ok {
				n, err := strconv.Atoi(s)
				if err != nil || n <= 0 {
					return c, fmt.Errorf("schedule %q: bad step %q", spec, s)
				}
				part, step = base, n
			}
			from, to := lo, hi
			if part != "*" {
				a, b, isRange := strings.Cut(part, "-")
				x, err := strconv.Atoi(a)
				if err != nil {
					return c, fmt.Errorf("schedule %q: bad value %q", spec, part)
				}
				from, to = x, x
				if isRange {
					if to, err = strconv.Atoi(b); err != nil {
						return c, fmt.Errorf("schedule %q: bad range %q", spec, part)
					}
				} else if step > 1 {
					to = hi
				}
			}
			if from < lo || to > hi || from > to {
				return c, fmt.Errorf("schedule %q: %q is out of range %d-%d", spec, part, lo, hi)
			}
			for v := from; v <= to; v += step {
				c[i][v] = true
			}
		}
	}
	return c, nil
}

func (c cron) matches(t time.Time) bool {
	return c[0][t.Minute()] && c[1][t.Hour()] && c[2][t.Day()] && c[3][int(t.Month())] && c[4][int(t.Weekday())]
}
