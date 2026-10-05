package f95

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	// DefaultSpacing is the minimum gap between the starts of two F95 requests (R-F95-5).
	DefaultSpacing = 5 * time.Second
	// DefaultInteractiveWait is how long an interactive caller waits for the lock.
	DefaultInteractiveWait = 60 * time.Second
	lockPoll               = 20 * time.Millisecond
)

// Pacer serializes F95 requests across processes: each request holds an
// exclusive flock on LockPath until Spacing after the request started.
type Pacer struct {
	LockPath string
	Spacing  time.Duration
	MaxWait  time.Duration // interactive wait cap; 0 = DefaultInteractiveWait
	pending  sync.WaitGroup
}

func NewPacer(lockPath string) *Pacer {
	return &Pacer{LockPath: lockPath, Spacing: DefaultSpacing, MaxWait: DefaultInteractiveWait}
}

// acquire takes the lock. Interactive callers give up with ErrBusy after MaxWait;
// others wait until ctx ends.
func (p *Pacer) acquire(ctx context.Context, interactive bool) (*os.File, error) {
	f, err := os.OpenFile(p.LockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("f95: open lock: %w", err)
	}
	var deadline <-chan time.Time
	if interactive {
		wait := p.MaxWait
		if wait <= 0 {
			wait = DefaultInteractiveWait
		}
		t := time.NewTimer(wait)
		defer t.Stop()
		deadline = t.C
	}
	tick := time.NewTicker(lockPoll)
	defer tick.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			f.Close()
			return nil, fmt.Errorf("f95: lock: %w", err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-deadline:
			f.Close()
			return nil, ErrBusy
		case <-tick.C:
		}
	}
}

// release frees the lock once Spacing has passed since started; it returns at once
// and the wait runs in the background (Wait joins it).
func (p *Pacer) release(f *os.File, started time.Time) {
	rest := p.Spacing - time.Since(started)
	if rest <= 0 {
		f.Close()
		return
	}
	p.pending.Add(1)
	go func() {
		defer p.pending.Done()
		time.Sleep(rest)
		f.Close() // closing the descriptor drops the flock
	}()
}

// Wait blocks until every pending spacing hold has been released; call before
// process exit so the next process does not start inside the window.
func (p *Pacer) Wait() { p.pending.Wait() }
