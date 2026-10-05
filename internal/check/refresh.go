// Package check holds the Source refresh step shared by the daily check, the
// manual Refresh button, Add Game and the CSV backfill (R-UPD-6).
package check

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
)

// check_result values written by Refresh.
const (
	StepDetail   = "detail"
	StepItchPage = "itch_page"

	OutcomeFetched   = "fetched"   // F95 detail fetch applied; first itch.io answer for a Source without a key
	OutcomeUnchanged = "unchanged" // itch.io page with the same change key
	OutcomeUpdate    = "update"    // itch.io change key differs
	OutcomeSkipped   = "skipped"   // nothing applied: not trackable, or no logged-in answer
	OutcomeMiss      = "miss"      // itch.io 404/410
	OutcomeError     = "error"
)

// Refresher performs one detail fetch of a primary Source and applies it.
type Refresher struct {
	store *db.Store
	clock clock.Clock
	games *games.Service
	tags  *tags.Service
	f95   *f95.Client
	itch  *itch.Client
	log   *slog.Logger
}

func NewRefresher(store *db.Store, clk clock.Clock, g *games.Service, t *tags.Service, f *f95.Client, i *itch.Client, log *slog.Logger) *Refresher {
	if log == nil {
		log = slog.Default()
	}
	return &Refresher{store: store, clock: clk, games: g, tags: t, f95: f, itch: i, log: log.With("component", "check")}
}

// RefreshResult describes what a successful Refresh did.
type RefreshResult struct {
	GameID        int64
	SourceID      int64
	CheckResultID int64
	Outcome       string // fetched, unchanged, update or skipped

	// itch.io only: the change keys of an Update (Outcome update).
	OldKey, NewKey string
	NotTrackable   bool // itch.io page without Updated row and uploads: checks were disabled (R-ITCH-5)

	// F95 only: Dev status before and after, for the digest row of an Update (R-UPD-8).
	OldDevStatus, NewDevStatus string
	Tags                       tags.MergeResult
	CoverFetched               bool
	CoverErr                   error // a failed cover download keeps the old cover and is not a Refresh error
}

// Update reports an itch.io Update.
func (r RefreshResult) Update() bool { return r.Outcome == OutcomeUpdate }

// Refresh fetches the primary Source once and applies the answer in one
// transaction, recording a check_result in run runID.
//
// F95 (logged in): name, Dev status (always overwritten, R-UPD-8), thread
// updated, cover URL and last_detail_at are written, details_pending is cleared,
// the F95 tags and Genre text are merged (tags.MergeRefreshTx grows the
// vocabulary), the queue row is removed and check_result(detail, fetched) is
// written. The version on the thread page is never compared and never raises an
// Update: F95 Updates come from checker.php alone. It only seeds latest_version
// and change_key when the Source has none yet (a baseline, no check_result).
// The cover downloads after the transaction.
//
// itch.io: latest_version (version token or NULL), change_key and
// thread_updated_at are written; a changed key is check_result(update) with
// old/new key. A page that cannot be tracked disables checks on the Source.
//
// On any fetch error nothing stored changes except a check_result(error) (a
// 404 from itch.io is 'miss'); the error is returned. Callers decide what
// ErrCookieInvalid, ErrRestricted, ErrBlocked, itch.ErrNotFound and ErrParse
// mean for their queue and run; miss_count and unavailable_at are not touched.
func (r *Refresher) Refresh(ctx context.Context, sourceID, runID int64) (RefreshResult, error) {
	src, err := r.store.Queries().GetSource(ctx, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshResult{}, fmt.Errorf("%w: source %d", domain.ErrNotFound, sourceID)
	}
	if err != nil {
		return RefreshResult{}, err
	}
	if src.IsPrimary != 1 {
		return RefreshResult{}, fmt.Errorf("%w: source %d is not primary", domain.ErrValidation, sourceID)
	}
	switch domain.SourceKind(src.Kind) {
	case domain.SourceF95Thread:
		return r.refreshF95(ctx, src, runID)
	case domain.SourceItchio:
		return r.refreshItch(ctx, src, runID)
	}
	return RefreshResult{}, fmt.Errorf("%w: source %d kind %q is not refreshed", domain.ErrValidation, sourceID, src.Kind)
}

func (r *Refresher) now() string { return clock.Timestamp(r.clock.Now()) }

// record writes a check_result outside the data transaction (failures).
func (r *Refresher) record(ctx context.Context, runID int64, src sqlcgen.Source, step, outcome string, cause error) {
	if ctx.Err() != nil {
		return // cancelled: not an observation about the Source
	}
	err := r.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := q.InsertCheckResult(ctx, resultParams(runID, src, step, outcome, "", "", cause.Error(), r.now()))
		return err
	})
	if err != nil {
		r.log.Error("record check result", "source_id", src.ID, "run_id", runID, "err", err)
	}
}

func resultParams(runID int64, src sqlcgen.Source, step, outcome, oldKey, newKey, errText, at string) sqlcgen.InsertCheckResultParams {
	return sqlcgen.InsertCheckResultParams{
		RunID: runID, SourceID: sql.NullInt64{Int64: src.ID, Valid: true}, Step: step, Outcome: outcome,
		OldKey: nullStr(oldKey), NewKey: nullStr(newKey), Attempts: 1, Error: nullStr(errText), At: at,
	}
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func (r *Refresher) refreshF95(ctx context.Context, src sqlcgen.Source, runID int64) (RefreshResult, error) {
	log := r.log.With("game_id", src.GameID, "source_id", src.ID, "run_id", runID)
	th, err := r.f95.FetchThread(ctx, src.ExternalID.String)
	if err != nil {
		r.record(ctx, runID, src, StepDetail, OutcomeError, err)
		log.Warn("detail fetch failed", "err", err)
		return RefreshResult{}, err
	}
	if !th.LoggedIn {
		// A guest page has partial tags and no Genre block: applying it would drop tags.
		err := fmt.Errorf("%w: no logged-in answer, detail fetch kept queued", f95.ErrCookieInvalid)
		r.record(ctx, runID, src, StepDetail, OutcomeSkipped, err)
		return RefreshResult{}, err
	}

	res := RefreshResult{GameID: src.GameID, SourceID: src.ID, Outcome: OutcomeFetched, OldDevStatus: src.DevStatus.String}
	detail := games.Detail{Name: &th.Name, DevStatus: &th.DevStatus, CoverURL: th.CoverURL, DetailAt: r.clock.Now()}
	if th.ThreadUpdated != "" {
		ts := th.ThreadUpdated + "T00:00:00Z"
		detail.ThreadUpdatedAt = &ts
	}
	if !src.ChangeKey.Valid {
		detail.LatestVersion, detail.ChangeKey = &th.Version, &th.Version
	}
	var applied games.DetailResult
	err = r.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if applied, err = r.games.ApplySourceDetail(ctx, q, src.ID, detail); err != nil {
			return err
		}
		if res.Tags, err = r.tags.MergeRefreshTx(ctx, q, src.GameID, th.GenreText, th.HasGenre, th.Tags); err != nil {
			return err
		}
		if err := q.DeleteDetailFetchQueue(ctx, src.ID); err != nil {
			return err
		}
		cr, err := q.InsertCheckResult(ctx, resultParams(runID, src, StepDetail, OutcomeFetched, "", "", "", r.now()))
		res.CheckResultID = cr.ID
		return err
	})
	if err != nil {
		r.record(ctx, runID, src, StepDetail, OutcomeError, err)
		return RefreshResult{}, err
	}
	res.NewDevStatus = string(th.DevStatus)

	if applied.FetchCover {
		if res.CoverErr = r.games.FetchCover(ctx, src.GameID, applied.CoverURL); res.CoverErr != nil {
			log.Warn("cover download failed, keeping the old cover", "err", res.CoverErr)
		} else {
			res.CoverFetched = true
		}
	}
	return res, nil
}

func (r *Refresher) refreshItch(ctx context.Context, src sqlcgen.Source, runID int64) (RefreshResult, error) {
	log := r.log.With("game_id", src.GameID, "source_id", src.ID, "run_id", runID)
	page, err := r.itch.FetchPage(ctx, src.Url)
	if err != nil {
		outcome := OutcomeError
		if errors.Is(err, itch.ErrNotFound) {
			outcome = OutcomeMiss
		}
		r.record(ctx, runID, src, StepItchPage, outcome, err)
		log.Warn("itch page fetch failed", "err", err)
		return RefreshResult{}, err
	}

	res := RefreshResult{GameID: src.GameID, SourceID: src.ID}
	err = r.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		at := r.now()
		if !page.Trackable {
			res.Outcome, res.NotTrackable = OutcomeSkipped, true
			if err := q.DisableSourceChecks(ctx, src.ID); err != nil {
				return err
			}
			return r.insertResult(ctx, q, &res, resultParams(runID, src, StepItchPage, OutcomeSkipped, "", "", "", at))
		}
		newKey := page.ChangeKey()
		p := resultParams(runID, src, StepItchPage, OutcomeFetched, "", newKey, "", at)
		switch {
		case !src.ChangeKey.Valid:
		case src.ChangeKey.String == newKey:
			p.Outcome, p.OldKey = OutcomeUnchanged, nullStr(newKey)
		default:
			p.Outcome, p.OldKey = OutcomeUpdate, src.ChangeKey
			res.OldKey, res.NewKey = src.ChangeKey.String, newKey
		}
		res.Outcome = p.Outcome
		apply := sqlcgen.ApplyItchPageParams{
			LatestVersion: nullStr(page.Token), ChangeKey: nullStr(newKey), At: nullStr(at), ID: src.ID,
		}
		if !page.Updated.IsZero() {
			apply.ThreadUpdatedAt = nullStr(clock.Timestamp(page.Updated))
		}
		if err := q.ApplyItchPage(ctx, apply); err != nil {
			return err
		}
		if err := q.DeleteDetailFetchQueue(ctx, src.ID); err != nil {
			return err
		}
		return r.insertResult(ctx, q, &res, p)
	})
	if err != nil {
		r.record(ctx, runID, src, StepItchPage, OutcomeError, err)
		return RefreshResult{}, err
	}
	return res, nil
}

func (r *Refresher) insertResult(ctx context.Context, q *sqlcgen.Queries, res *RefreshResult, p sqlcgen.InsertCheckResultParams) error {
	cr, err := q.InsertCheckResult(ctx, p)
	res.CheckResultID = cr.ID
	return err
}

// StartManualRun opens a check_run(kind='manual') for an interactive fetch
// (Add Game, the Refresh button). finish closes it with status ok, partial or
// failed; call it exactly once.
func (r *Refresher) StartManualRun(ctx context.Context) (runID int64, finish func(status string), err error) {
	var run sqlcgen.CheckRun
	err = r.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		run, err = q.InsertCheckRun(ctx, sqlcgen.InsertCheckRunParams{Kind: "manual", StartedAt: r.now()})
		return err
	})
	if err != nil {
		return 0, nil, err
	}
	bg := context.WithoutCancel(ctx)
	return run.ID, func(status string) {
		if status != "ok" && status != "partial" {
			status = "failed"
		}
		err := r.store.WithTx(bg, func(q *sqlcgen.Queries) error {
			return q.FinishCheckRun(bg, sqlcgen.FinishCheckRunParams{Status: status, FinishedAt: nullStr(r.now()), ID: run.ID})
		})
		if err != nil {
			r.log.Error("finish manual run", "run_id", run.ID, "err", err)
		}
	}, nil
}
