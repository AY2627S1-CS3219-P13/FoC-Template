package schedule

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

var Singapore = time.FixedZone("Asia/Singapore", 8*60*60)
var ErrHours = errors.New("invalid opening hours: use weekday rules and 24-hour intervals")

func ValidateWindow(start, now time.Time) error {
	s := start.In(Singapore)
	if start.IsZero() || s.Hour() < 9 || s.Hour() > 20 || s.Minute()%30 != 0 || s.Second() != 0 || s.Nanosecond() != 0 || start.Before(now.Add(time.Hour)) {
		return errors.New("choose a half-hour slot from 09:00 to 21:00 at least one hour ahead")
	}
	return nil
}

type interval struct{ start, end int }

func minute(s string, end bool) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, ErrHours
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrHours
		}
	}
	h, e := strconv.Atoi(s[:2])
	if e != nil {
		return 0, ErrHours
	}
	m, e := strconv.Atoi(s[3:])
	if e != nil || h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && (!end || m != 0)) {
		return 0, ErrHours
	}
	return h*60 + m, nil
}
func HoursCover(text string, start, end time.Time) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return true, nil
	}
	text = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(text)), "–", "-")
	days := map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	rules := map[int][]interval{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false, ErrHours
		}
		selected := []int{}
		for _, group := range strings.Split(fields[0], ",") {
			ends := strings.Split(group, "-")
			if len(ends) > 2 {
				return false, ErrHours
			}
			lo, ok := days[ends[0]]
			if !ok {
				return false, ErrHours
			}
			hi := lo
			if len(ends) == 2 {
				var ok bool
				hi, ok = days[ends[1]]
				if !ok || (lo == 0 && hi != 0) || (hi != 0 && hi < lo) {
					return false, ErrHours
				}
				if hi == 0 && lo != 0 {
					hi = 7
				}
			}
			for d := lo; d <= hi; d++ {
				selected = append(selected, d%7)
			}
		}
		ints := []interval{}
		if fields[1] != "closed" {
			for _, part := range strings.Split(fields[1], ",") {
				pair := strings.Split(part, "-")
				if len(pair) != 2 {
					return false, ErrHours
				}
				lo, e := minute(pair[0], false)
				if e != nil {
					return false, e
				}
				hi, e := minute(pair[1], true)
				if e != nil || lo >= hi {
					return false, ErrHours
				}
				ints = append(ints, interval{lo, hi})
			}
		}
		for _, day := range selected {
			if previous, ok := rules[day]; ok && (len(previous) == 0 || len(ints) == 0) {
				return false, ErrHours
			}
			rules[day] = append(rules[day], ints...)
		}
	}
	for day, ints := range rules {
		sort.Slice(ints, func(i, j int) bool { return ints[i].start < ints[j].start })
		merged := []interval{}
		for _, v := range ints {
			if len(merged) > 0 {
				last := &merged[len(merged)-1]
				if v.start < last.end {
					return false, ErrHours
				}
				if v.start == last.end {
					last.end = v.end
					continue
				}
			}
			merged = append(merged, v)
		}
		rules[day] = merged
	}
	s, e := start.In(Singapore), end.In(Singapore)
	if !end.After(start) || s.Format("2006-01-02") != e.Add(-time.Nanosecond).Format("2006-01-02") {
		return false, ErrHours
	}
	lo, hi := s.Hour()*60+s.Minute(), e.Hour()*60+e.Minute()
	if e.Day() != s.Day() {
		hi = 1440
	}
	for _, v := range rules[int(s.Weekday())] {
		if lo >= v.start && hi <= v.end {
			return true, nil
		}
	}
	return false, nil
}
func AvailableSlots(date string, now time.Time, pickupHours, deliveryHours string) ([]time.Time, error) {
	day, err := time.ParseInLocation("2006-01-02", date, Singapore)
	if err != nil {
		return nil, err
	}
	result := []time.Time{}
	for i := 0; i < 24; i++ {
		s := day.Add(time.Duration(9*60+i*30) * time.Minute)
		p, e := HoursCover(pickupHours, s, s.Add(30*time.Minute))
		if e != nil {
			return nil, e
		}
		d, e := HoursCover(deliveryHours, s, s.Add(30*time.Minute))
		if e != nil {
			return nil, e
		}
		if ValidateWindow(s, now) == nil && p && d {
			result = append(result, s)
		}
	}
	return result, nil
}
