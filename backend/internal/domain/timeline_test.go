package domain

import (
	"testing"
	"time"
)

func TestDayStarts(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 1, 30, 0, 0, time.UTC) // 2026-11-02 22:30 in São Paulo
	starts, end := DayStarts(now, loc, 3)
	want := []string{"2026-10-31", "2026-11-01", "2026-11-02"}
	if len(starts) != len(want) {
		t.Fatalf("len %d", len(starts))
	}
	for i, s := range starts {
		if s.Format(time.DateOnly) != want[i] || s.Hour() != 0 || s.Location() != loc {
			t.Errorf("start %d: %v", i, s)
		}
	}
	if !end.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, loc)) {
		t.Errorf("end: %v", end)
	}
	if one, end1 := DayStarts(now, loc, 1); len(one) != 1 || !end1.Equal(end) || one[0].Format(time.DateOnly) != "2026-11-02" {
		t.Errorf("one day: %v %v", one, end1)
	}
}
