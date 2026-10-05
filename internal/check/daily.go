package check

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/tags"
)

const (
	StepChecker     = "checker"
	StepCookieProbe = "cookie_probe"

	maxMisses        = 3 // consecutive misses before the confirming fetch (R-UPD-5)
	checkerBatch     = 100
	dailyDetailCap   = 40 // routine detail fetches per host-local day (R-UPD-6)
	weeklyBatch      = 20
	weeklyAge        = 7 * 24 * time.Hour
	f95CheckerSource = "F95 checker"
)

// Deps are the collaborators of the daily run. The F95 client must be built
// for check mode (Interactive false, Retry f95.DefaultRetry).
type Deps struct {
	Store     *db.Store
	Clock     clock.Clock
	Tags      *tags.Service
	Notify    *notify.Notifier
	F95       *f95.Client
	Creds     *f95.CredStore
	Itch      *itch.Client
	Refresher *Refresher
	Log       *slog.Logger
	LockPath  string         // <stateDir>/check.lock
	Local     *time.Location // day boundary of the detail cap; default time.Local
}

// Runner performs the daily check (R-UPD-2).
type Runner struct{ Deps }

func NewRunner(d Deps) *Runner {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	d.Log = d.Log.With("component", "check")
	if d.Local == nil {
		d.Local = time.Local
	}
	return &Runner{d}
}

// Options are the `check` flags.
type Options struct {
	NoDigest    bool
	OnlyChecker bool // checker.php step (and the digest) only
}

type pendingUpdate struct {
	sourceID int64
	item     notify.UpdateItem
}

type runState struct {
	id          int64
	f95Stopped  bool
	itchStopped bool
	successes   int
	failed      []notify.FailedItem
	updates     []pendingUpdate
	devStatus   map[int64][2]string // source id -> Dev status before, after a detail fetch
}

func (st *runState) fail(gameID, resultID int64, source string, err error) {
	st.failed = append(st.failed, notify.FailedItem{GameID: gameID, CheckResultID: resultID, Source: source, Error: err.Error(), New: true})
}

func (r *Runner) now() string { return clock.Timestamp(r.Clock.Now()) }

// Run performs one daily check. It returns nil when another run holds
// check.lock and when F95 or itch.io fail (failures are reported in the
// digest, R-UPD-11); errors are infrastructure failures only.
func (r *Runner) Run(ctx context.Context, o Options) error {
	lock, err := os.OpenFile(r.LockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("check: open lock: %w", err)
	}
	defer lock.Close() // closing drops the flock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			r.Log.Info("another check run holds the lock, exiting")
			return nil
		}
		return fmt.Errorf("check: lock: %w", err)
	}

	if n, err := r.Notify.RetryUnsent(ctx); err != nil {
		r.Log.Warn("retry unsent notifications", "err", err)
	} else if n > 0 {
		r.Log.Info("re-sent unsent notifications", "count", n)
	}

	var run sqlcgen.CheckRun
	err = r.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		run, err = q.InsertCheckRun(ctx, sqlcgen.InsertCheckRunParams{Kind: "daily", StartedAt: r.now()})
		return err
	})
	if err != nil {
		return err
	}
	st := &runState{id: run.ID, devStatus: map[int64][2]string{}}
	runErr := r.steps(ctx, st, o)

	status := "ok"
	switch {
	case runErr != nil || (len(st.failed) > 0 && st.successes == 0):
		status = "failed"
	case len(st.failed) > 0:
		status = "partial"
	}
	bg := context.WithoutCancel(ctx)
	stopped := int64(0)
	if st.f95Stopped {
		stopped = 1
	}
	finishErr := r.Store.WithTx(bg, func(q *sqlcgen.Queries) error {
		return q.FinishCheckRun(bg, sqlcgen.FinishCheckRunParams{
			Status: status, FinishedAt: sql.NullString{String: r.now(), Valid: true}, F95Stopped: stopped, ID: run.ID,
		})
	})
	r.Log.Info("check run finished", "run_id", run.ID, "status", status, "updates", len(st.updates), "failed", len(st.failed), "f95_stopped", st.f95Stopped)
	return errors.Join(runErr, finishErr)
}

func (r *Runner) steps(ctx context.Context, st *runState, o Options) error {
	if !o.OnlyChecker {
		if err := r.enqueueWeekly(ctx); err != nil {
			return err
		}
	}
	if err := r.checker(ctx, st); err != nil {
		return err
	}
	if !o.OnlyChecker {
		if err := r.itchPages(ctx, st); err != nil {
			return err
		}
		if err := r.probe(ctx, st); err != nil {
			return err
		}
		if err := r.details(ctx, st); err != nil {
			return err
		}
	}
	if o.NoDigest {
		return nil
	}
	return r.digest(ctx, st)
}

// enqueueWeekly queues the rolling refresh of Sources not fetched for a week (R-UPD-6).
func (r *Runner) enqueueWeekly(ctx context.Context) error {
	cutoff := clock.Timestamp(r.Clock.Now().Add(-weeklyAge))
	return r.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		ids, err := q.ListWeeklyCandidates(ctx, sqlcgen.ListWeeklyCandidatesParams{LastDetailAt: sql.NullString{String: cutoff, Valid: true}, Limit: weeklyBatch})
		if err != nil {
			return err
		}
		for _, id := range ids {
			err := q.EnqueueDetailFetch(ctx, sqlcgen.EnqueueDetailFetchParams{SourceID: id, Reason: "weekly", Budget: "routine", EnqueuedAt: r.now()})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// checker asks checker.php about all checkable F95 primaries (R-UPD-3, R-UPD-5).
func (r *Runner) checker(ctx context.Context, st *runState) error {
	srcs, err := r.Store.Queries().ListCheckableSources(ctx, "f95_thread")
	if err != nil {
		return err
	}
	for start := 0; start < len(srcs) && !st.f95Stopped; start += checkerBatch {
		batch := srcs[start:min(start+checkerBatch, len(srcs))]
		ids := make([]string, len(batch))
		for i, s := range batch {
			ids[i] = s.ExternalID.String
		}
		answers, err := r.F95.CheckVersions(ctx, ids)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			resultID, rerr := r.recordBatchError(ctx, st.id, batch, err)
			if rerr != nil {
				return rerr
			}
			if errors.Is(err, f95.ErrBlocked) {
				st.f95Stopped = true
			}
			r.Log.Warn("checker.php failed", "err", err, "f95_stopped", st.f95Stopped)
			st.fail(0, resultID, f95CheckerSource, err)
			continue
		}
		if err := r.applyAnswers(ctx, st, batch, answers); err != nil {
			return err
		}
		st.successes++
	}
	return nil
}

func (r *Runner) recordBatchError(ctx context.Context, runID int64, batch []sqlcgen.ListCheckableSourcesRow, cause error) (first int64, err error) {
	err = r.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		for _, s := range batch {
			row, err := q.InsertCheckResult(ctx, sqlcgen.InsertCheckResultParams{
				RunID: runID, SourceID: sql.NullInt64{Int64: s.ID, Valid: true}, Step: StepChecker, Outcome: OutcomeError,
				Attempts: 1, Error: nullStr(cause.Error()), At: r.now(),
			})
			if err != nil {
				return err
			}
			if first == 0 {
				first = row.ID
			}
		}
		return nil
	})
	return first, err
}

// applyAnswers writes one checker.php answer for batch in one transaction.
func (r *Runner) applyAnswers(ctx context.Context, st *runState, batch []sqlcgen.ListCheckableSourcesRow, answers map[string]string) error {
	var updates []pendingUpdate
	err := r.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		updates = updates[:0]
		at := r.now()
		for _, s := range batch {
			p := sqlcgen.InsertCheckResultParams{RunID: st.id, SourceID: sql.NullInt64{Int64: s.ID, Valid: true}, Step: StepChecker, Attempts: 1, At: at}
			v, ok := answers[s.ExternalID.String]
			if !ok {
				if _, err := q.IncrementSourceMiss(ctx, s.ID); err != nil {
					return err
				}
				p.Outcome, p.OldKey = OutcomeMiss, s.ChangeKey
				if _, err := q.InsertCheckResult(ctx, p); err != nil {
					return err
				}
				continue
			}
			if err := q.MarkSourceChecked(ctx, sqlcgen.MarkSourceCheckedParams{LastCheckedAt: nullStr(at), ID: s.ID}); err != nil {
				return err
			}
			p.NewKey = nullStr(v)
			switch {
			case !s.ChangeKey.Valid: // no baseline yet: seed it, not an Update (R-UPD-4)
				p.Outcome = OutcomeFetched
				if err := q.SetSourceBaseline(ctx, sqlcgen.SetSourceBaselineParams{LatestVersion: nullStr(v), ChangeKey: nullStr(v), ID: s.ID}); err != nil {
					return err
				}
			case s.ChangeKey.String == v:
				p.Outcome, p.OldKey = OutcomeUnchanged, s.ChangeKey
			default:
				p.Outcome, p.OldKey = OutcomeUpdate, s.ChangeKey
				if err := q.SetSourceBaseline(ctx, sqlcgen.SetSourceBaselineParams{LatestVersion: nullStr(v), ChangeKey: nullStr(v), ID: s.ID}); err != nil {
					return err
				}
				err := q.EnqueueDetailFetch(ctx, sqlcgen.EnqueueDetailFetchParams{SourceID: s.ID, Reason: "update", Budget: "routine", EnqueuedAt: at})
				if err != nil {
					return err
				}
			}
			row, err := q.InsertCheckResult(ctx, p)
			if err != nil {
				return err
			}
			if p.Outcome == OutcomeUpdate && s.Alerting == 1 {
				updates = append(updates, pendingUpdate{s.ID, notify.UpdateItem{
					GameID: s.GameID, CheckResultID: row.ID, Name: s.GameName, OldVersion: s.ChangeKey.String, NewVersion: v, New: true,
				}})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	st.updates = append(st.updates, updates...)
	return nil
}

// itchPages fetches every checkable itch.io primary through the Refresher (R-ITCH-1).
func (r *Runner) itchPages(ctx context.Context, st *runState) error {
	srcs, err := r.Store.Queries().ListCheckableSources(ctx, "itchio")
	if err != nil {
		return err
	}
	for _, s := range srcs {
		if st.itchStopped {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := r.Refresher.Refresh(ctx, s.ID, st.id)
		switch {
		case err == nil:
			st.successes++
			if res.Update() && s.Alerting == 1 {
				if err := r.itchUpdate(ctx, st, s, res); err != nil {
					return err
				}
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, itch.ErrNotFound):
			if err := r.itchMiss(ctx, s); err != nil {
				return err
			}
		case errors.Is(err, itch.ErrBlocked):
			st.itchStopped = true
			st.fail(0, 0, "itch.io", err)
		default:
			st.fail(s.GameID, 0, s.GameName, err)
		}
	}
	return nil
}

func (r *Runner) itchUpdate(ctx context.Context, st *runState, s sqlcgen.ListCheckableSourcesRow, res RefreshResult) error {
	after, err := r.Store.Queries().GetSource(ctx, s.ID)
	if err != nil {
		return err
	}
	item := notify.UpdateItem{
		GameID: s.GameID, CheckResultID: res.CheckResultID, Name: s.GameName, New: true,
		OldVersion: itchLabel(s.LatestVersion, s.ThreadUpdatedAt), NewVersion: itchLabel(after.LatestVersion, after.ThreadUpdatedAt),
	}
	// The devlog title is optional: its failure never fails the check (R-ITCH-7).
	if title, err := r.Itch.DevlogTitle(ctx, s.Url); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.Log.Warn("itch devlog", "source_id", s.ID, "err", err)
	} else if title != "" {
		item.NewVersion += fmt.Sprintf(" (devlog: %s)", title)
	}
	st.updates = append(st.updates, pendingUpdate{s.ID, item})
	return nil
}

// itchLabel is the version token, else the Updated date, of an itch.io Source.
func itchLabel(version, updated sql.NullString) string {
	switch {
	case version.Valid:
		return version.String
	case len(updated.String) >= 10:
		return updated.String[:10]
	}
	return "unknown"
}

// itchMiss counts a 404/410; the third in a row is itself the confirmation (R-ITCH-6).
func (r *Runner) itchMiss(ctx context.Context, s sqlcgen.ListCheckableSourcesRow) error {
	var n int64
	err := r.write(ctx, func(q *sqlcgen.Queries) (err error) {
		n, err = q.IncrementSourceMiss(ctx, s.ID)
		return err
	})
	if err != nil {
		return err
	}
	if n >= maxMisses {
		return r.markUnavailable(ctx, s.ID, s.GameName, "itch.io page returned 404/410 on 3 consecutive checks")
	}
	return nil
}

func (r *Runner) write(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	return r.Store.WithTx(ctx, fn)
}

func (r *Runner) markUnavailable(ctx context.Context, sourceID int64, gameName, reason string) error {
	err := r.write(ctx, func(q *sqlcgen.Queries) error {
		return q.MarkSourceUnavailable(ctx, sqlcgen.MarkSourceUnavailableParams{UnavailableAt: nullStr(r.now()), UnavailableReason: nullStr(reason), ID: sourceID})
	})
	if err != nil {
		return err
	}
	r.Log.Info("source marked unavailable", "source_id", sourceID, "reason", reason)
	if err := r.Notify.SourceUnavailable(ctx, sourceID, gameName); err != nil {
		r.Log.Warn("source unavailable push", "source_id", sourceID, "err", err)
	}
	return nil
}

// credential reports whether a cookie is stored and whether it is marked invalid.
func (r *Runner) credential(ctx context.Context) (have, invalid bool, err error) {
	c, err := r.Creds.Get(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, c.Validity == "invalid", err
}

// cookieInvalid records the flip and sends the one push per invalidation (R-NOTIF-4).
func (r *Runner) cookieInvalid(ctx context.Context) {
	if _, err := r.Creds.MarkInvalid(ctx); err != nil {
		r.Log.Warn("mark cookie invalid", "err", err)
	}
	if err := r.Notify.CookieInvalid(ctx); err != nil {
		r.Log.Warn("cookie invalid push", "err", err)
	}
}

// probe loads one thread with the stored cookie (R-UPD-2 step 3).
func (r *Runner) probe(ctx context.Context, st *runState) error {
	have, _, err := r.credential(ctx)
	if err != nil || !have || st.f95Stopped {
		return err
	}
	thread, err := r.Creds.ProbeThread(ctx)
	if err != nil {
		return err
	}
	ok, perr := r.F95.Probe(ctx, thread)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p := sqlcgen.InsertCheckResultParams{RunID: st.id, Step: StepCookieProbe, Outcome: OutcomeFetched, Attempts: 1, At: r.now()}
	switch {
	case perr != nil:
		p.Outcome, p.Error = OutcomeError, nullStr(perr.Error())
	case !ok:
		p.Outcome, p.Error = OutcomeError, nullStr("cookie invalid")
	}
	if err := r.Store.WithTx(ctx, func(q *sqlcgen.Queries) error { _, err := q.InsertCheckResult(ctx, p); return err }); err != nil {
		return err
	}
	switch {
	case perr != nil:
		if errors.Is(perr, f95.ErrBlocked) {
			st.f95Stopped = true
		}
		st.fail(0, 0, "F95 cookie check", perr)
	case !ok:
		r.cookieInvalid(ctx)
	}
	return nil
}

// details runs the confirming fetches and drains the routine queue (R-UPD-5..7).
func (r *Runner) details(ctx context.Context, st *runState) error {
	have, invalid, err := r.credential(ctx)
	if err != nil || !have || invalid || st.f95Stopped {
		return err // no cookie / invalid: the queue stays intact (R-UPD-7)
	}
	stop, err := r.confirm(ctx, st)
	if err != nil || stop {
		return err
	}
	return r.drain(ctx, st)
}

// confirm does the one confirming detail fetch of each Source with 3 misses (R-UPD-5).
func (r *Runner) confirm(ctx context.Context, st *runState) (stop bool, err error) {
	srcs, err := r.Store.Queries().ListCheckableSources(ctx, "f95_thread")
	if err != nil {
		return false, err
	}
	for _, s := range srcs {
		if s.MissCount < maxMisses {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		res, err := r.Refresher.Refresh(ctx, s.ID, st.id)
		switch {
		case err == nil:
			st.devStatus[s.ID] = [2]string{res.OldDevStatus, res.NewDevStatus}
			if err := r.write(ctx, func(q *sqlcgen.Queries) error { return q.ResetSourceMisses(ctx, s.ID) }); err != nil {
				return false, err
			}
		case errors.Is(err, f95.ErrRestricted):
			if err := r.markUnavailable(ctx, s.ID, s.GameName, "thread gone or restricted (confirming fetch)"); err != nil {
				return false, err
			}
		default:
			if stop, err := r.detailError(ctx, st, s.ID, s.GameID, s.GameName, err); stop || err != nil {
				return stop, err
			}
		}
	}
	return false, nil
}

// detailError classifies a failed non-restricted detail fetch; stop ends all F95 requests of the run.
func (r *Runner) detailError(ctx context.Context, st *runState, sourceID, gameID int64, name string, err error) (stop bool, _ error) {
	switch {
	case ctx.Err() != nil:
		return true, ctx.Err()
	case errors.Is(err, f95.ErrCookieInvalid):
		r.cookieInvalid(ctx)
		return true, nil
	case errors.Is(err, f95.ErrBlocked):
		st.f95Stopped = true
		st.fail(gameID, 0, name, err)
		return true, nil
	}
	st.fail(gameID, 0, name, err)
	return false, nil
}

// drain works the routine detail queue within the daily cap.
func (r *Runner) drain(ctx context.Context, st *runState) error {
	now := r.Clock.Now().In(r.Local)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, r.Local)
	used, err := r.Store.Queries().CountDailyDetailResults(ctx, sqlcgen.CountDailyDetailResultsParams{
		At: clock.Timestamp(dayStart), At_2: clock.Timestamp(dayStart.AddDate(0, 0, 1)),
	})
	if err != nil {
		return err
	}
	ids, err := r.Store.Queries().ListRoutineQueue(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if used >= dailyDetailCap {
			r.Log.Info("daily detail cap reached, queue kept", "cap", dailyDetailCap)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		src, err := r.Store.Queries().GetSource(ctx, id)
		if err != nil {
			return err
		}
		name, err := r.gameName(ctx, src.GameID)
		if err != nil {
			return err
		}
		res, err := r.Refresher.Refresh(ctx, id, st.id)
		used++
		switch {
		case err == nil:
			st.devStatus[id] = [2]string{res.OldDevStatus, res.NewDevStatus}
			st.successes++
		case errors.Is(err, f95.ErrRestricted):
			// Unreadable is not a transient failure: drop the row, the weekly refresh retries.
			st.fail(src.GameID, 0, name, err)
			if err := r.write(ctx, func(q *sqlcgen.Queries) error { return q.DeleteDetailFetchQueue(ctx, id) }); err != nil {
				return err
			}
		default:
			if stop, err := r.detailError(ctx, st, id, src.GameID, name, err); stop || err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) gameName(ctx context.Context, gameID int64) (string, error) {
	g, err := r.Store.Queries().GetGame(ctx, gameID)
	return g.Name, err
}

// digest builds and sends the daily digest (R-NOTIF-2, R-NOTIF-5).
func (r *Runner) digest(ctx context.Context, st *runState) error {
	d := notify.Digest{RunID: st.id, CheckFailed: st.failed}
	for _, u := range st.updates {
		if dev, ok := st.devStatus[u.sourceID]; ok {
			u.item.OldDevStatus, u.item.NewDevStatus = dev[0], dev[1]
		}
		d.Updates = append(d.Updates, u.item)
	}
	_, d.NeedsYou.CookieInvalid, _ = r.credential(ctx)
	queue, err := r.Tags.Queue(ctx)
	if err != nil {
		return err
	}
	d.NeedsYou.TagsToReview = len(queue)
	if _, err := r.Notify.SendDigest(ctx, d); err != nil {
		r.Log.Warn("digest not delivered", "err", err) // a failed digest is recorded, not retried (R-NOTIF-1)
	}
	return nil
}
