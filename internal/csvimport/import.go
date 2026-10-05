package csvimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/tags"
)

// ErrPaused means the F95 baseline or backfill stopped on a block signal or an
// invalid cookie; the queue is intact and a re-run resumes (R-CSV-9).
var ErrPaused = errors.New("import paused")

// Importer runs the import. F95 must be a check-mode client (retry ladder, not
// interactive); its pacer enforces the 1 request / 5 s of the backfill.
type Importer struct {
	Store     *db.Store
	Clock     clock.Clock
	Games     *games.Service
	Tags      *tags.Service
	F95       *f95.Client
	Refresher *check.Refresher
	Log       *slog.Logger
}

// Summary counts what a run did. Rows are CSV lines.
type Summary struct {
	Rows      int // read
	Games     int // created now
	Merged    int // rows folded into another row's Game (same thread, R-CSV-6)
	Skipped   int // rows whose Game an earlier run already imported
	Converted int // Games created from restricted threads, as manual Sources
	Baselined int // F95 Games given their checker.php baseline now

	BackfillDone      int // detail fetches completed now
	BackfillRemaining int // F95 Games of the sheet still without a detail fetch
}

// Run imports the sheet. With backfill false it stops after the baseline.
// A paused run returns the summary so far and an error wrapping ErrPaused.
func (im *Importer) Run(ctx context.Context, sheet io.Reader, backfill bool) (Summary, error) {
	rows, err := Parse(sheet)
	if err != nil {
		return Summary{}, err
	}
	sum := Summary{Rows: len(rows)}

	var order []string
	groups := map[string][]Row{}
	for _, r := range rows {
		if _, ok := groups[r.key()]; !ok {
			order = append(order, r.key())
		}
		groups[r.key()] = append(groups[r.key()], r)
	}
	for _, k := range order {
		if err := im.write(ctx, groups[k], &sum); err != nil {
			return sum, fmt.Errorf("csv line %d (%s): %w", groups[k][0].Line, groups[k][0].Name, err)
		}
	}

	var f95Sources []sqlcgen.Source
	for _, k := range order {
		if g := groups[k]; g[0].F95() {
			src, err := im.Store.Queries().FindSourceByExternal(ctx, sqlcgen.FindSourceByExternalParams{
				Kind: string(domain.SourceF95Thread), ExternalID: sql.NullString{String: g[0].Spec.ExternalID, Valid: true},
			})
			if err != nil {
				return sum, err
			}
			f95Sources = append(f95Sources, src)
		}
	}

	if err := im.baseline(ctx, f95Sources, &sum); err != nil {
		return sum, err
	}
	if !backfill {
		sum.BackfillRemaining = im.pending(ctx, f95Sources)
		return sum, nil
	}
	return sum, im.backfill(ctx, f95Sources, &sum)
}

// write imports one Game (all rows sharing a thread or link) in one transaction.
// Rows are in file order: the last supplies the rating and last played version
// (R-CSV-6); each row with a version is one imported Play log entry.
func (im *Importer) write(ctx context.Context, rs []Row, sum *Summary) error {
	last := rs[len(rs)-1]
	played := false
	for _, r := range rs {
		played = played || r.Version != ""
	}
	p := games.CreateParams{
		Name:         rs[0].Name, // provisional; the backfill title replaces it for F95 Games (R-CSV-2)
		PlayStatus:   derivePlayStatus(played, last.DevStatus),
		RatingX2:     last.RatingX2,
		ImportReview: true,
		Source:       last.Spec,
	}
	if !last.F95() {
		p.DevStatus = last.DevStatus
	}
	created := false
	err := im.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		c, err := im.Games.CreateTx(ctx, q, p)
		if errors.Is(err, domain.ErrConflict) {
			return nil
		}
		if err != nil {
			return err
		}
		created = true
		now := clock.Timestamp(im.Clock.Now())
		for _, r := range rs {
			if r.Version == "" {
				continue
			}
			if _, err := q.InsertPlayLog(ctx, sqlcgen.InsertPlayLogParams{
				GameID: c.Game.ID, Version: r.Version, Origin: string(domain.PlayLogImported), CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		if browserOnly[last.Spec.URL] {
			if err := q.DisableSourceChecks(ctx, c.Source.ID); err != nil {
				return err
			}
		}
		if last.F95() {
			return im.Tags.EnsurePending(ctx, q, c.Game.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !created {
		sum.Skipped += len(rs)
		return nil
	}
	sum.Games++
	sum.Merged += len(rs) - 1
	if last.Converted {
		sum.Converted++
	}
	return nil
}

// baseline stores the live version of F95 Games that have none, from one
// checker.php pass, with no check_result (R-UPD-4). Threads F95 does not answer
// get their baseline from the backfill fetch.
func (im *Importer) baseline(ctx context.Context, srcs []sqlcgen.Source, sum *Summary) error {
	var ids []string
	byID := map[string]sqlcgen.Source{}
	for _, s := range srcs {
		if !s.ChangeKey.Valid {
			ids = append(ids, s.ExternalID.String)
			byID[s.ExternalID.String] = s
		}
	}
	if len(ids) == 0 {
		return nil
	}
	versions, err := im.F95.CheckVersions(ctx, ids)
	if err != nil {
		return fmt.Errorf("baseline: %w", pause(err))
	}
	err = im.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		for id, v := range versions {
			if err := im.Games.SetBaseline(ctx, q, byID[id].ID, v, v); err != nil {
				return err
			}
			sum.Baselined++
		}
		return nil
	})
	return err
}

// pending counts the sheet's F95 Games that still lack a detail fetch.
func (im *Importer) pending(ctx context.Context, srcs []sqlcgen.Source) int {
	n := 0
	for _, s := range srcs {
		if cur, err := im.Store.Queries().GetSource(ctx, s.ID); err != nil || !cur.LastDetailAt.Valid {
			n++
		}
	}
	return n
}

// backfill enqueues and drains the detail fetches (R-CSV-9).
func (im *Importer) backfill(ctx context.Context, srcs []sqlcgen.Source, sum *Summary) (err error) {
	var todo []sqlcgen.Source
	err = im.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		for _, s := range srcs {
			cur, err := q.GetSource(ctx, s.ID)
			if err != nil {
				return err
			}
			if cur.LastDetailAt.Valid {
				continue
			}
			todo = append(todo, cur)
			if err := q.EnqueueDetailFetch(ctx, sqlcgen.EnqueueDetailFetchParams{
				SourceID: cur.ID, Reason: "import", Budget: "import", EnqueuedAt: clock.Timestamp(im.Clock.Now()),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(todo) == 0 {
		return err
	}

	var run sqlcgen.CheckRun
	err = im.Store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		run, err = q.InsertCheckRun(ctx, sqlcgen.InsertCheckRunParams{Kind: "import_backfill", StartedAt: clock.Timestamp(im.Clock.Now())})
		return err
	})
	if err != nil {
		return err
	}
	failed, stopped := 0, false
	defer func() {
		status := "ok"
		switch {
		case stopped && sum.BackfillDone == 0:
			status = "failed"
		case stopped || failed > 0:
			status = "partial"
		}
		bg := context.WithoutCancel(ctx)
		ferr := im.Store.WithTx(bg, func(q *sqlcgen.Queries) error {
			stop := int64(0)
			if stopped {
				stop = 1
			}
			return q.FinishCheckRun(bg, sqlcgen.FinishCheckRunParams{
				Status: status, FinishedAt: sql.NullString{String: clock.Timestamp(im.Clock.Now()), Valid: true},
				F95Stopped: stop, ID: run.ID,
			})
		})
		if ferr != nil {
			im.Log.Error("finish import backfill run", "run_id", run.ID, "err", ferr)
		}
		sum.BackfillRemaining = im.pending(bg, srcs)
	}()

	for _, s := range todo {
		if _, err := im.Refresher.Refresh(ctx, s.ID, run.ID); err != nil {
			if p := pause(err); errors.Is(p, ErrPaused) {
				stopped = true
				return fmt.Errorf("backfill: %w", p)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++ // restricted or unparseable thread: recorded by Refresh, stays queued
			im.Log.Warn("backfill fetch failed", "thread", s.ExternalID.String, "err", err)
			continue
		}
		sum.BackfillDone++
		if err := im.rederive(ctx, s.GameID); err != nil {
			return err
		}
	}
	return nil
}

// rederive sets the Play status from the live Dev status while the Game is
// still on the import review list (R-CSV-4); a confirmed Game is left alone.
func (im *Importer) rederive(ctx context.Context, gameID int64) error {
	q := im.Store.Queries()
	g, err := q.GetGame(ctx, gameID)
	if err != nil || g.ImportReview != 1 {
		return err
	}
	src, err := q.GetPrimarySource(ctx, gameID)
	if err != nil {
		return err
	}
	log, err := q.ListPlayLog(ctx, gameID)
	if err != nil {
		return err
	}
	ps := derivePlayStatus(len(log) > 0, domain.DevStatus(src.DevStatus.String))
	if string(ps) == g.PlayStatus {
		return nil
	}
	return im.Games.SetPlayStatus(ctx, gameID, ps)
}

// pause wraps block signals and an invalid cookie in ErrPaused.
func pause(err error) error {
	if errors.Is(err, f95.ErrBlocked) || errors.Is(err, f95.ErrCookieInvalid) || errors.Is(err, f95.ErrBusy) {
		return fmt.Errorf("%w: %w", ErrPaused, err)
	}
	return err
}
