package tags

import (
	"errors"
	"testing"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func TestReviewPrefillAndSave(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	pl := testutil.InsertPlayLog(t, e.store, g, "v1", "2026-02-01")
	// harem: both (prefill correct); titfuck: genre only (unset); vore: earlier verdict wrong;
	// pregnancy: planned.
	e.add(t, g, "Harem, Titfuck, Vore\nPlanned: Pregnancy", list("harem"))
	rows := e.tags(t, g)
	if err := e.svc.SetVerification(ctx, rows["vore"].ID, Wrong); err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.ReviewModel(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	verdict := map[string]string{}
	for _, r := range m.Present {
		verdict[r.Tag.Slug] = r.Verdict
	}
	if verdict["harem"] != VerdictCorrect || verdict["titfuck"] != "" || verdict["vore"] != VerdictWrong || len(m.Present) != 3 {
		t.Errorf("prefill = %v", verdict)
	}
	if len(m.Other) != 1 || m.Other[0].Tag.Slug != "pregnancy" || m.Other[0].Verdict != "" {
		t.Errorf("other = %+v", m.Other)
	}

	// Partial save keeps the Game queued.
	res, err := e.svc.SaveReview(ctx, g, map[int64]string{rows["harem"].ID: VerdictCorrect})
	if err != nil || res.Done || res.Remaining != 1 {
		t.Fatalf("partial = %+v %v", res, err)
	}
	if tr, _ := e.store.Queries().GetTagReview(ctx, g); tr.State != ReviewPending {
		t.Errorf("state after partial = %s", tr.State)
	}
	if got := e.tags(t, g)["harem"]; got.Verification != Confirmed || !got.VerifiedAt.Valid {
		t.Errorf("harem = %+v", got)
	}

	// Complete: planned tag "now present", flags cleared, play log recorded.
	e.merge(t, g, "Harem, Titfuck, Vore, Creampie", true, list("harem"))
	if r := e.tags(t, g)["creampie"]; r.IsNew != 1 {
		t.Fatal("creampie not new")
	}
	res, err = e.svc.SaveReview(ctx, g, map[int64]string{
		rows["titfuck"].ID: VerdictCorrect, rows["pregnancy"].ID: VerdictNowPresent, e.tags(t, g)["creampie"].ID: VerdictWrong,
	})
	if err != nil || !res.Done {
		t.Fatalf("complete = %+v %v", res, err)
	}
	got := e.tags(t, g)
	if r := got["pregnancy"]; r.Qualifier != QualPresent || r.Verification != Confirmed {
		t.Errorf("pregnancy = %+v", r)
	}
	if r := got["creampie"]; r.Verification != Wrong || r.IsNew != 0 {
		t.Errorf("creampie = %+v", r)
	}
	tr, _ := e.store.Queries().GetTagReview(ctx, g)
	if tr.State != ReviewDone || !tr.ReviewedAt.Valid || tr.LastReviewedPlayLogID.Int64 != pl.ID {
		t.Errorf("review row = %+v", tr)
	}
	if q, _ := e.svc.Queue(ctx); len(q) != 0 {
		t.Errorf("done Game still queued: %+v", q)
	}
	if m, _ := e.svc.ReviewModel(ctx, g); !m.Later {
		t.Error("completed review must mark later reviews")
	}
}

func TestReviewVerdictValidation(t *testing.T) {
	e := newEnv(t)
	g, other := e.game(t), e.game(t)
	e.add(t, g, "Harem\nPlanned: Pregnancy", nil)
	e.add(t, other, "Harem", nil)
	rows := e.tags(t, g)
	if _, err := e.svc.SaveReview(ctx, g, map[int64]string{rows["pregnancy"].ID: VerdictCorrect}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("correct on planned err = %v", err)
	}
	if _, err := e.svc.SaveReview(ctx, g, map[int64]string{e.tags(t, other)["harem"].ID: VerdictCorrect}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("foreign tag err = %v", err)
	}
	if got := e.tags(t, g)["pregnancy"]; got.Verification != Unverified {
		t.Error("failed save must not write")
	}
}

func TestReviewRemovedAtSource(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem, Titfuck, Vore", nil)
	e.merge(t, g, "Harem", true, nil)
	rows := e.tags(t, g)
	m, _ := e.svc.ReviewModel(ctx, g)
	removed := 0
	for _, r := range m.Present {
		if r.RemovedAtSource {
			removed++
		}
	}
	if removed != 2 {
		t.Errorf("removed lines = %d", removed)
	}
	res, err := e.svc.SaveReview(ctx, g, map[int64]string{
		rows["harem"].ID: VerdictCorrect, rows["titfuck"].ID: VerdictCorrect, rows["vore"].ID: VerdictWrong,
	})
	if err != nil || !res.Done {
		t.Fatalf("%+v %v", res, err)
	}
	got := e.tags(t, g)
	if r := got["titfuck"]; r.RemovedAtSourceAt.Valid || r.Origin != OriginManual || r.Verification != Confirmed {
		t.Errorf("kept removed tag = %+v", r)
	}
	if _, ok := got["vore"]; ok {
		t.Error("dismissed wrong tag still present")
	}
}

func TestSkipAndQueue(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.game(t), e.game(t), e.game(t)
	testutil.InsertPlayLog(t, e.store, b, "v2", "2026-03-01")
	for _, g := range []int64{a, b, c} {
		e.add(t, g, "Harem", nil)
	}
	err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		for _, g := range []int64{a, b} {
			if err := e.svc.EnsurePending(ctx, q, g); err != nil {
				return err
			}
		}
		return e.svc.EnsurePending(ctx, q, a) // idempotent
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SkipReview(ctx, b); err != nil {
		t.Fatal(err)
	}
	q, err := e.svc.Queue(ctx)
	if err != nil || len(q) != 2 {
		t.Fatalf("queue = %+v %v", q, err)
	}
	states := map[int64]string{}
	for _, r := range q {
		states[r.GameID] = r.State
		if r.GameID == b && (r.LastPlayedVersion.String != "v2" || r.UnverifiedCount != 1 || r.PresentCount != 1) {
			t.Errorf("row b = %+v", r)
		}
	}
	if states[a] != ReviewPending || states[b] != ReviewSkipped {
		t.Errorf("states = %v", states)
	}
	// EnsurePending never reopens a finished review.
	if _, err := e.svc.SaveReview(ctx, a, map[int64]string{e.tags(t, a)["harem"].ID: VerdictCorrect}); err != nil {
		t.Fatal(err)
	}
	_ = e.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return e.svc.EnsurePending(ctx, q, a) })
	if tr, _ := e.store.Queries().GetTagReview(ctx, a); tr.State != ReviewDone {
		t.Errorf("state = %s", tr.State)
	}
}

func TestReviewDue(t *testing.T) {
	e := newEnv(t)
	empty := e.game(t)
	testutil.InsertPlayLog(t, e.store, empty, "v1", "2026-02-01")
	if due, first, _ := e.svc.ReviewDue(ctx, empty); due || !first {
		t.Errorf("no tags: due=%v first=%v", due, first)
	}

	g := e.game(t)
	e.add(t, g, "Harem, Titfuck", nil)
	testutil.InsertPlayLog(t, e.store, g, "v1", "2026-02-01")
	if due, first, _ := e.svc.ReviewDue(ctx, g); !due || !first {
		t.Errorf("first entry with tags: due=%v first=%v", due, first)
	}
	rows := e.tags(t, g)
	if _, err := e.svc.SaveReview(ctx, g, map[int64]string{rows["harem"].ID: VerdictCorrect, rows["titfuck"].ID: VerdictCorrect}); err != nil {
		t.Fatal(err)
	}
	testutil.InsertPlayLog(t, e.store, g, "v2", "2026-03-01")
	if due, first, _ := e.svc.ReviewDue(ctx, g); due || first {
		t.Errorf("reviewed, nothing new: due=%v first=%v", due, first)
	}
	e.merge(t, g, "Harem, Titfuck, Vore", true, nil)
	if due, first, _ := e.svc.ReviewDue(ctx, g); !due || first {
		t.Errorf("new tag since review: due=%v first=%v", due, first)
	}
	// A completed review is not "first" again after deleting entries down to one.
	if _, err := e.svc.SaveReview(ctx, g, map[int64]string{e.tags(t, g)["vore"].ID: VerdictCorrect}); err != nil {
		t.Fatal(err)
	}
	if due, _, _ := e.svc.ReviewDue(ctx, g); due {
		t.Error("flags cleared by the review")
	}
}

func TestHandEdits(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem, Titfuck", nil)
	rows := e.tags(t, g)
	e.clk.Advance(3600e9)
	if err := e.svc.SetVerification(ctx, rows["harem"].ID, Confirmed); err != nil {
		t.Fatal(err)
	}
	if r := e.tags(t, g)["harem"]; r.Verification != Confirmed || r.VerifiedAt.String != "2026-10-05T09:00:00Z" {
		t.Errorf("harem = %+v", r)
	}
	if err := e.svc.SetVerification(ctx, rows["harem"].ID, "maybe"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("err = %v", err)
	}
	if err := e.svc.SetQualifier(ctx, rows["titfuck"].ID, QualOptional); err != nil {
		t.Fatal(err)
	}
	if r := e.tags(t, g)["titfuck"]; r.Qualifier != QualOptional || r.Verification != Unverified {
		t.Errorf("titfuck = %+v", r)
	}
	// Adding by hand a tag the Game already has confirms it and keeps the stronger qualifier.
	if _, err := e.svc.AddByHand(ctx, g, TagRef{Kind: KindF95, Slug: "titfuck"}, QualPlanned); err != nil {
		t.Fatal(err)
	}
	if r := e.tags(t, g)["titfuck"]; r.Qualifier != QualOptional || r.Verification != Confirmed || r.Origin != OriginGenre {
		t.Errorf("titfuck after add = %+v", r)
	}
	// "Harem" is itself an F95 slug: the override applies but no Synonym is saved for it.
	if _, err := e.svc.SetMapping(ctx, e.tags(t, g)["harem"].ID, TagRef{Kind: KindF95, Slug: "ntr"}); err != nil {
		t.Fatal(err)
	}
	rows2, _ := e.svc.ListSynonyms(ctx, "")
	for _, s := range rows2 {
		if s.PhraseKey == "harem" {
			t.Errorf("slug phrase saved a synonym: %+v", s)
		}
	}
	if err := e.svc.DismissRemoved(ctx, e.tags(t, g)["ntr"].ID); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("dismiss of a present tag err = %v", err)
	}
	e.merge(t, g, "Titfuck", true, nil)
	// The override row is only kept while its phrase stays in the Genre text; "Harem" is gone.
	gone := e.tags(t, g)["ntr"]
	if !gone.RemovedAtSourceAt.Valid {
		t.Fatalf("ntr = %+v", gone)
	}
	if err := e.svc.DismissRemoved(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.tags(t, g)["ntr"]; ok {
		t.Error("dismissed tag remains")
	}
}
