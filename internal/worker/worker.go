// Package worker is the downloader daemon: it polls the tracker for jobs,
// downloads from the job's mirrors, extracts into the games directory and
// reports progress.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/f95-tracker/internal/worker/api"
)

type Options struct {
	Client           *api.Client
	GamesDir         string
	PollInterval     time.Duration
	ProgressInterval time.Duration // minimum spacing of progress events
	Hosts            map[string]Host
	Log              *slog.Logger
}

type Worker struct {
	Options
	incoming string
}

// New creates <games-dir>/.incoming.
func New(o Options) (*Worker, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	w := &Worker{Options: o, incoming: filepath.Join(o.GamesDir, ".incoming")}
	return w, os.MkdirAll(w.incoming, 0o755)
}

// Run polls until ctx is done. Tracker outages are logged and retried.
func (w *Worker) Run(ctx context.Context) {
	for {
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.PollInterval):
		}
	}
}

// RunOnce polls once and processes the returned jobs sequentially.
// Sequential processing also gives per-host concurrency 1.
func (w *Worker) RunOnce(ctx context.Context) error {
	resp, err := w.Client.Jobs(ctx)
	if err != nil {
		return err
	}
	for _, id := range resp.Cancelled {
		w.removeJobFiles(id)
	}
	for _, job := range resp.Jobs {
		if ctx.Err() != nil {
			return nil
		}
		w.process(ctx, job)
	}
	return nil
}

func (w *Worker) removeJobFiles(id int64) {
	matches, _ := filepath.Glob(filepath.Join(w.incoming, strconv.FormatInt(id, 10)+"-*"))
	for _, m := range matches {
		os.RemoveAll(m)
	}
	os.Remove(w.markerPath(id))
}

// markerPath records a finished install whose done report may not have
// reached the tracker; it holds the result path.
func (w *Worker) markerPath(id int64) string {
	return filepath.Join(w.incoming, "installed-"+strconv.FormatInt(id, 10))
}

type trackerErr struct{ error }

func (e *trackerErr) Unwrap() error { return e.error }

type run struct {
	w     *Worker
	job   api.Job
	mu    sync.Mutex
	state string // job state as the tracker last reported it
}

func (w *Worker) process(ctx context.Context, job api.Job) {
	log := w.Log.With("job", job.ID, "game", job.Game.Name)
	jctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stop local work when the tracker cancelled or closed the job
		defer wg.Done()
		t := time.NewTicker(w.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-jctx.Done():
				return
			case <-t.C:
			}
			resp, err := w.Client.Jobs(jctx)
			if err != nil {
				continue
			}
			if slices.Contains(resp.Cancelled, job.ID) ||
				!slices.ContainsFunc(resp.Jobs, func(j api.Job) bool { return j.ID == job.ID }) {
				cancel(errJobCancelled)
				return
			}
		}
	}()

	r := &run{w: w, job: job, state: job.State}
	err := r.execute(jctx)
	cancel(nil)
	wg.Wait()

	switch {
	case errors.Is(context.Cause(jctx), errJobCancelled):
		w.removeJobFiles(job.ID)
		log.Info("job cancelled or closed on the tracker; local work stopped")
	case ctx.Err() != nil:
		log.Info("shutting down; job left resumable")
	case err != nil:
		log.Warn("job aborted", "err", err)
	}
}

func (r *run) send(ctx context.Context, ev api.Event) (api.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.State == r.state {
		ev.State = "" // same-state events are not transitions
	}
	job, err := r.w.Client.PostEvent(ctx, r.job.ID, ev)
	if err != nil {
		return job, &trackerErr{err}
	}
	if job.State != "" {
		r.state = job.State
	}
	return job, nil
}

func (r *run) execute(ctx context.Context) error {
	if r.state == "extracting" {
		// Left over from a stopped worker. The tracker has no extracting ->
		// downloading edge: finish if the install completed, else fall through.
		if b, err := os.ReadFile(r.w.markerPath(r.job.ID)); err == nil {
			return r.finish(ctx, string(b), nil)
		}
		if _, err := r.send(ctx, api.Event{State: "queued"}); err != nil {
			return err
		}
	}
	if files := r.w.findDropped(r.job.ID); len(files) > 0 {
		return r.installDropped(ctx, files)
	}
	var mirrors []api.Mirror
	for _, m := range r.job.Mirrors {
		if m.Supported {
			mirrors = append(mirrors, m)
		}
	}
	slices.SortStableFunc(mirrors, func(a, b api.Mirror) int { return a.Position - b.Position })
	if len(mirrors) == 0 {
		_, err := r.send(ctx, api.Event{State: "needs_human", NeedsHumanReason: "no supported mirror"})
		return err
	}
	for i, m := range mirrors {
		err := r.tryMirror(ctx, m)
		if err == nil {
			return nil
		}
		var te *trackerErr
		if errors.As(err, &te) || ctx.Err() != nil {
			return err
		}
		r.w.Log.Warn("mirror failed", "job", r.job.ID, "mirror", m.ID, "host", m.Host, "err", err)
		ev := api.Event{MirrorID: &m.ID, MirrorState: "failed", Error: err.Error()}
		if i < len(mirrors)-1 {
			ev.State = "queued"
		}
		resp, serr := r.send(ctx, ev)
		if serr != nil {
			return serr
		}
		if resp.State == "needs_human" {
			return nil
		}
	}
	return nil
}

func (r *run) tryMirror(ctx context.Context, m api.Mirror) error {
	host := r.w.Hosts[m.Host]
	if host == nil {
		return fmt.Errorf("no adapter for host %q", m.Host)
	}
	if _, err := r.send(ctx, api.Event{State: "downloading", ActiveMirrorID: &m.ID, MirrorID: &m.ID, MirrorState: "active"}); err != nil {
		return err
	}
	dir := filepath.Join(r.w.incoming, fmt.Sprintf("%d-%d", r.job.ID, m.ID))
	last := time.Now()
	progress := func(done, total int64) {
		if time.Since(last) < r.w.ProgressInterval {
			return
		}
		last = time.Now()
		ev := api.Event{BytesDone: &done}
		if total > 0 {
			ev.BytesTotal = &total
		}
		_, _ = r.send(ctx, ev) // progress is best effort
	}
	if err := host.Download(ctx, m.URL, dir, progress); err != nil {
		if ctx.Err() == nil {
			os.RemoveAll(dir)
		}
		return err
	}
	if _, err := r.send(ctx, api.Event{State: "extracting"}); err != nil {
		return err
	}
	files, err := listFiles(dir)
	if err != nil {
		return err
	}
	dest := r.dest()
	if err := r.installTo(ctx, files, dest, fmt.Sprintf("%d-%d", r.job.ID, m.ID)); err != nil {
		if ctx.Err() == nil {
			os.RemoveAll(dir)
		}
		return err
	}
	return r.finish(ctx, dest, &m.ID)
}

// finish reports done for an installed result. The marker survives until the
// tracker has accepted the report.
func (r *run) finish(ctx context.Context, dest string, mirrorID *int64) error {
	if err := os.WriteFile(r.w.markerPath(r.job.ID), []byte(dest), 0o644); err != nil {
		return err
	}
	ev := api.Event{State: "done", ResultPath: dest}
	if mirrorID != nil {
		ev.MirrorID, ev.MirrorState = mirrorID, "done"
	}
	if _, err := r.send(ctx, ev); err != nil {
		return err
	}
	r.w.removeJobFiles(r.job.ID)
	return nil
}

func (r *run) dest() string {
	return filepath.Join(r.w.GamesDir, SanitizeName(r.job.Game.Name), SanitizeName(r.job.TargetVersion))
}

// installTo removes a destination it created itself if extraction fails.
func (r *run) installTo(ctx context.Context, files []string, dest, stageName string) error {
	_, statErr := os.Stat(dest)
	links, err := install(ctx, files, dest, filepath.Join(r.w.incoming, stageName, "x"))
	if links > 0 {
		r.w.Log.Warn("removed symlinks from extracted archive", "job", r.job.ID, "count", links)
	}
	if err != nil && os.IsNotExist(statErr) {
		os.RemoveAll(dest)
	}
	return err
}

// findDropped returns files the user dropped in .incoming for this job:
// a directory <jobId>/ (any contents) or files named <jobId>.<anything>.
func (w *Worker) findDropped(id int64) []string {
	name := strconv.FormatInt(id, 10)
	entries, _ := os.ReadDir(w.incoming)
	var files []string
	for _, e := range entries {
		p := filepath.Join(w.incoming, e.Name())
		switch {
		case e.Name() == name && e.IsDir():
			sub, _ := listFiles(p)
			files = append(files, sub...)
		case e.Name() == name && e.Type().IsRegular(), strings.HasPrefix(e.Name(), name+".") && e.Type().IsRegular():
			files = append(files, p)
		}
	}
	return files
}

func (r *run) installDropped(ctx context.Context, files []string) error {
	r.w.Log.Info("installing user-dropped archive", "job", r.job.ID)
	if _, err := r.send(ctx, api.Event{State: "downloading"}); err != nil {
		return err
	}
	if _, err := r.send(ctx, api.Event{State: "extracting"}); err != nil {
		return err
	}
	dest := r.dest()
	if err := r.installTo(ctx, files, dest, fmt.Sprintf("%d-drop", r.job.ID)); err != nil {
		if ctx.Err() != nil {
			return err
		}
		_, serr := r.send(ctx, api.Event{State: "needs_human", NeedsHumanReason: "dropped archive: " + err.Error()})
		return serr
	}
	name := strconv.FormatInt(r.job.ID, 10)
	os.RemoveAll(filepath.Join(r.w.incoming, name))
	for _, f := range files {
		if filepath.Dir(f) == r.w.incoming {
			os.Remove(f)
		}
	}
	return r.finish(ctx, dest, nil)
}
