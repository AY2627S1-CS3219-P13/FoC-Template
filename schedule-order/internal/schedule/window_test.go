package schedule

import (
	"testing"
	"time"
)

func instant(s string) time.Time {
	t, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return t
}
func TestWindowNoticeGridAndFutureDates(t *testing.T) {
	start := instant("2026-11-03T09:00:00+08:00")
	for _, tc := range []struct {
		name       string
		start, now time.Time
		valid      bool
	}{
		{"exact hour", start, start.Add(-time.Hour), true},
		{"one second short", start, start.Add(-time.Hour + time.Second), false},
		{"off grid", start.Add(time.Minute), start.Add(-2 * time.Hour), false},
		{"before opening", start.Add(-30 * time.Minute), start.Add(-2 * time.Hour), false},
		{"last slot", start.Add(690 * time.Minute), start.Add(-2 * time.Hour), true},
		{"too late", start.Add(720 * time.Minute), start.Add(-2 * time.Hour), false},
		{"future year", start.AddDate(4, 0, 0), start, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWindow(tc.start, tc.now)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
func TestHoursGrammarAndWholeWindow(t *testing.T) {
	start := instant("2026-11-03T09:00:00+08:00") // Tuesday
	for _, tc := range []struct {
		hours string
		start time.Time
		open  bool
		valid bool
	}{
		{"", start, true, true},
		{"Mon–Fri 09:00–17:00", start, true, true},
		{"mon-fri 09:00-17:00", start.Add(450 * time.Minute), true, true},
		{"Tue 09:00-09:30", start, true, true},
		{"Tue 09:15-17:00", start, false, true},
		{"Tue 09:00-09:29", start, false, true},
		{"Mon 00:00-24:00", start, false, true},
		{"Tue Closed", start, false, true},
		{"Tue 09:00-12:00,13:00-17:00", start.Add(180 * time.Minute), false, true},
		{"Tue 09:00-09:15,09:15-10:00", start, true, true},
		{"Tue 00:00-24:00", start, true, true},
		{"Tue 09:00-17:00 garbage", start, false, false},
		{"Tue 09:00-17:00\nTue Closed", start, false, false},
		{"Fri-Mon 09:00-17:00", start, false, false},
		{"Tue 22:00-02:00", start, false, false},
		{"Tue 09:00-13:00,12:00-17:00", start, false, false},
		{"9-5", start, false, false},
	} {
		t.Run(tc.hours, func(t *testing.T) {
			open, err := HoursCover(tc.hours, tc.start, tc.start.Add(30*time.Minute))
			if (err == nil) != tc.valid || (err == nil && open != tc.open) {
				t.Fatalf("open=%v err=%v", open, err)
			}
		})
	}
}
func TestSlotsUseSingaporeDateAndNotice(t *testing.T) {
	slots, err := AvailableSlots("2026-11-03", instant("2026-11-03T08:00:01+08:00"), "", "")
	if err != nil || len(slots) != 23 || slots[0].Format(time.RFC3339) != "2026-11-03T09:30:00+08:00" {
		t.Fatalf("slots=%v err=%v", slots, err)
	}
	slots, err = AvailableSlots("2026-11-04", instant("2026-11-03T20:45:00+08:00"), "Wed 10:00-11:00", "Wed 10:30-12:00")
	if err != nil || len(slots) != 1 || slots[0].Hour() != 10 || slots[0].Minute() != 30 {
		t.Fatalf("slots=%v err=%v", slots, err)
	}
	if _, err = AvailableSlots("invalid", time.Now(), "", ""); err == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestHoursSundayRangeAndStrictDigits(t *testing.T) {
	tue := instant("2026-11-03T09:00:00+08:00")
	if open, err := HoursCover("Sun-Sun 09:00-17:00", tue, tue.Add(30*time.Minute)); err != nil || open {
		t.Fatalf("Sunday range included Tuesday: open=%v err=%v", open, err)
	}
	if _, err := HoursCover("Tue +9:00-17:00", tue, tue.Add(30*time.Minute)); err == nil {
		t.Fatal("non-digit clock accepted")
	}
}
