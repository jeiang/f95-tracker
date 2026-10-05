package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	start := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	f := NewFake(start)
	f.Advance(90 * time.Minute)
	if got := f.Now(); !got.Equal(start.Add(90 * time.Minute)) {
		t.Fatalf("Advance: %v", got)
	}
	f.Set(start)
	if !f.Now().Equal(start) {
		t.Fatal("Set")
	}
}

func TestFormats(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	in := time.Date(2026, 1, 2, 1, 4, 5, 999, loc) // 2026-01-01T23:04:05Z
	if got := Timestamp(in); got != "2026-01-01T23:04:05Z" {
		t.Fatalf("Timestamp = %q", got)
	}
	if got := Date(in); got != "2026-01-01" {
		t.Fatalf("Date = %q", got)
	}
	ts, err := ParseTimestamp("2026-01-01T23:04:05Z")
	if err != nil || Timestamp(ts) != "2026-01-01T23:04:05Z" {
		t.Fatalf("ParseTimestamp: %v %v", ts, err)
	}
	d, err := ParseDate("2026-01-01")
	if err != nil || Date(d) != "2026-01-01" {
		t.Fatalf("ParseDate: %v %v", d, err)
	}
	if _, err := ParseDate("2026-1-1x"); err == nil {
		t.Fatal("want error")
	}
}
