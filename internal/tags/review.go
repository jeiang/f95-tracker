package tags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// Review verdicts (R-TAG-9).
const (
	VerdictCorrect    = "correct"
	VerdictWrong      = "wrong"
	VerdictNowPresent = "now_present" // planned/optional tags only
)

// Review states as stored in tag_review.
const (
	ReviewPending = "pending"
	ReviewSkipped = "skipped"
	ReviewDone    = "done"
)

// ReviewTag is one line of the Tag review checklist.
type ReviewTag struct {
	GameTagID       int64
	Tag             TagRef
	Origin          string
	Qualifier       string
	Note            string
	F95Only         bool
	Verdict         string // prefilled: earlier verdict kept, else origin both → correct, else ""
	IsNew           bool
	Promoted        bool
	RemovedAtSource bool
	Changed         bool // new, promoted, removed at source, or still unverified: what a later review asks about
}

// ReviewModel is the data behind /games/{id}/review (R-UI-5).
type ReviewModel struct {
	GameID  int64
	State   string // "" when no review row exists yet
	Later   bool   // a review was completed before: show only Changed tags first
	Present []ReviewTag
	Other   []ReviewTag // planned and optional: the collapsible, skippable section
}

// QueueRow is one Game of the tags-to-review queue.
type QueueRow = sqlcgen.ListReviewQueueRow

// EnsurePending creates the Game's tag_review row as pending if it has none
// (CSV import, first Play log); an existing row is left alone (INV-18).
func (s *Service) EnsurePending(ctx context.Context, q *sqlcgen.Queries, gameID int64) error {
	return q.EnsureTagReview(ctx, sqlcgen.EnsureTagReviewParams{GameID: gameID, UpdatedAt: s.now()})
}

// ReviewDue decides the redirect after a Play log entry was added (R-UI-4,
// R-TAG-8). firstReview: this is the Game's first user entry and no review was
// completed yet; then the review is due when the Game has present tags. Later,
// it is due when tags are new, promoted or removed at source, or unreviewed
// present tags remain and the review is not done.
func (s *Service) ReviewDue(ctx context.Context, gameID int64) (due, firstReview bool, err error) {
	q := s.store.Queries()
	entries, err := q.CountUserPlayLog(ctx, gameID)
	if err != nil {
		return false, false, err
	}
	tr, err := q.GetTagReview(ctx, gameID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	done := err == nil && tr.State == ReviewDone
	firstReview = entries <= 1 && !done
	if firstReview {
		n, err := q.CountPresentTags(ctx, gameID)
		return n > 0, true, err
	}
	changed, err := q.CountChangedTags(ctx, gameID)
	if err != nil || changed > 0 {
		return changed > 0, false, err
	}
	if done {
		return false, false, nil
	}
	unreviewed, err := q.CountUnreviewedPresentTags(ctx, gameID)
	return unreviewed > 0, false, err
}

// ReviewModel builds the checklist with the R-TAG-8 prefill.
func (s *Service) ReviewModel(ctx context.Context, gameID int64) (ReviewModel, error) {
	q := s.store.Queries()
	if _, err := q.GetGame(ctx, gameID); err != nil {
		return ReviewModel{}, mapNoRows(err, "game", gameID)
	}
	m := ReviewModel{GameID: gameID}
	tr, err := q.GetTagReview(ctx, gameID)
	switch {
	case err == nil:
		m.State, m.Later = tr.State, tr.ReviewedAt.Valid
	case !errors.Is(err, sql.ErrNoRows):
		return m, err
	}
	rows, err := q.ListGameTags(ctx, gameID)
	if err != nil {
		return m, err
	}
	for _, r := range rows {
		t := ReviewTag{
			GameTagID: r.ID, Tag: TagRef{Kind: r.TagKind, Slug: r.TagSlug, Label: r.TagLabel}, Origin: r.Origin,
			Qualifier: r.Qualifier, Note: r.ModifierNote.String, F95Only: r.F95Only == 1, IsNew: r.IsNew == 1,
			Promoted: r.Promoted == 1, RemovedAtSource: r.RemovedAtSourceAt.Valid,
		}
		t.Changed = t.IsNew || t.Promoted || t.RemovedAtSource || r.Verification == Unverified
		switch {
		case r.Verification == Confirmed && r.Qualifier == QualPresent:
			t.Verdict = VerdictCorrect
		case r.Verification == Wrong:
			t.Verdict = VerdictWrong
		case r.Verification == Unverified && r.Origin == OriginBoth && r.Qualifier == QualPresent:
			t.Verdict = VerdictCorrect
		}
		if r.Qualifier == QualPresent {
			m.Present = append(m.Present, t)
		} else {
			m.Other = append(m.Other, t)
		}
	}
	return m, nil
}

// ReviewResult tells the caller where the Game ended up.
type ReviewResult struct {
	Done      bool
	Remaining int // present tags still without a verdict
}

// SaveReview applies verdicts keyed by Game tag id (R-TAG-9). Present tags:
// correct → confirmed, wrong → wrong. Planned/optional: now_present → present +
// confirmed, wrong → wrong. A tag removed at source is dismissed by any verdict:
// correct keeps it as the user's own manual tag, wrong drops it. When every
// present tag has a verdict the review is done and is_new/promoted are cleared;
// otherwise the Game stays (or becomes) pending.
func (s *Service) SaveReview(ctx context.Context, gameID int64, verdicts map[int64]string) (ReviewResult, error) {
	var res ReviewResult
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetGame(ctx, gameID); err != nil {
			return mapNoRows(err, "game", gameID)
		}
		now := s.now()
		for id, v := range verdicts {
			if v == "" {
				continue
			}
			row, err := s.gameTagTx(ctx, q, id)
			if err != nil {
				return err
			}
			if row.GameID != gameID {
				return notFound("game tag", id)
			}
			if err := applyVerdict(ctx, q, row, v, now); err != nil {
				return err
			}
		}
		rows, err := q.ListGameTags(ctx, gameID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Qualifier == QualPresent && !r.RemovedAtSourceAt.Valid && r.Verification == Unverified {
				res.Remaining++
			}
		}
		if res.Remaining > 0 {
			return q.SetTagReviewState(ctx, sqlcgen.SetTagReviewStateParams{GameID: gameID, State: ReviewPending, UpdatedAt: now})
		}
		var last sql.NullInt64
		lp, err := q.GetGameLastPlayed(ctx, gameID)
		switch {
		case err == nil:
			last = sql.NullInt64{Int64: lp.PlayLogID, Valid: true}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := q.CompleteTagReview(ctx, sqlcgen.CompleteTagReviewParams{GameID: gameID, PlayLogID: last, At: nullStr(now)}); err != nil {
			return err
		}
		res.Done = true
		return q.ClearGameTagFlags(ctx, gameID)
	})
	return res, err
}

func applyVerdict(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.GameTag, verdict, now string) error {
	if verdict != VerdictCorrect && verdict != VerdictWrong && verdict != VerdictNowPresent {
		return invalid("verdict %q", verdict)
	}
	if row.RemovedAtSourceAt.Valid {
		if verdict == VerdictWrong {
			return q.DeleteGameTag(ctx, row.ID)
		}
		row.Origin, row.RemovedAtSourceAt, row.F95Only = OriginManual, sql.NullString{}, 0
		row.Verification, row.VerifiedAt = Confirmed, nullStr(now)
		if verdict == VerdictNowPresent {
			row.Qualifier = QualPresent
		}
		return q.UpdateGameTag(ctx, updateParams(row))
	}
	switch {
	case verdict == VerdictWrong:
		row.Verification = Wrong
	case row.Qualifier == QualPresent && verdict == VerdictCorrect:
		row.Verification = Confirmed
	case row.Qualifier != QualPresent && verdict == VerdictNowPresent:
		row.Verification, row.Qualifier = Confirmed, QualPresent
	default:
		return fmt.Errorf("%w: verdict %q does not apply to a %s tag", domain.ErrValidation, verdict, row.Qualifier)
	}
	row.VerifiedAt = nullStr(now)
	return q.UpdateGameTag(ctx, updateParams(row))
}

// SkipReview puts the Game into the queue as skipped without writing verdicts.
func (s *Service) SkipReview(ctx context.Context, gameID int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetGame(ctx, gameID); err != nil {
			return mapNoRows(err, "game", gameID)
		}
		return q.SetTagReviewState(ctx, sqlcgen.SetTagReviewStateParams{GameID: gameID, State: ReviewSkipped, UpdatedAt: s.now()})
	})
}

// Queue lists the tags-to-review queue: one row per Game, pending or skipped.
func (s *Service) Queue(ctx context.Context) ([]QueueRow, error) {
	return s.store.Queries().ListReviewQueue(ctx)
}
