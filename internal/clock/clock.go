// Package clock is the only source of "now"; time-dependent code takes a Clock.
package clock

import (
	"sync"
	"time"
)

const (
	timestampLayout = "2006-01-02T15:04:05Z"
	dateLayout      = "2006-01-02"
)

type Clock interface{ Now() time.Time }

// Real reads the system clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake is a settable clock, safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

func NewFake(t time.Time) *Fake { return &Fake{now: t} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// Timestamp formats t as the stored UTC text form 2006-01-02T15:04:05Z.
func Timestamp(t time.Time) string { return t.UTC().Format(timestampLayout) }

// ParseTimestamp is the inverse of Timestamp.
func ParseTimestamp(s string) (time.Time, error) { return time.Parse(timestampLayout, s) }

// Date formats t (UTC) as YYYY-MM-DD.
func Date(t time.Time) string { return t.UTC().Format(dateLayout) }

// ParseDate parses YYYY-MM-DD as midnight UTC.
func ParseDate(s string) (time.Time, error) { return time.Parse(dateLayout, s) }
