package tags

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

var ctx = context.Background()

type env struct {
	store *db.Store
	clk   *clock.Fake
	svc   *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := testutil.NewStore(t)
	clk := clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC))
	return &env{store: st, clk: clk, svc: New(st, clk)}
}

func (e *env) game(t *testing.T) int64 {
	t.Helper()
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{})
	return g.ID
}

func thread(t *testing.T, id string) *f95.Thread {
	t.Helper()
	b, err := os.ReadFile("../../testdata/f95/" + id + ".html")
	if err != nil {
		t.Fatal(err)
	}
	th, err := f95.ParseThread(b, id, "https://f95zone.to/threads/"+id+"/", true)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// add runs the parse check and applies it unedited.
func (e *env) add(t *testing.T, gameID int64, text string, list []f95.Tag) ParseCheckModel {
	t.Helper()
	m, err := e.svc.ParseCheck(ctx, text, list)
	if err != nil {
		t.Fatal(err)
	}
	e.apply(t, gameID, m)
	return m
}

func (e *env) apply(t *testing.T, gameID int64, m ParseCheckModel) {
	t.Helper()
	if err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return e.svc.ApplyAddTx(ctx, q, gameID, m) }); err != nil {
		t.Fatal(err)
	}
}

func (e *env) merge(t *testing.T, gameID int64, text string, hasGenre bool, list []f95.Tag) MergeResult {
	t.Helper()
	var res MergeResult
	err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) (err error) {
		res, err = e.svc.MergeRefreshTx(ctx, q, gameID, text, hasGenre, list)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// tags returns the Game's tags by slug.
func (e *env) tags(t *testing.T, gameID int64) map[string]sqlcgen.ListGameTagsRow {
	t.Helper()
	rows, err := e.svc.GameTags(ctx, gameID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]sqlcgen.ListGameTagsRow{}
	for _, r := range rows {
		out[r.TagSlug] = r
	}
	return out
}

func list(slugs ...string) []f95.Tag {
	out := make([]f95.Tag, len(slugs))
	for i, s := range slugs {
		out[i] = f95.Tag{Slug: s, Label: s}
	}
	return out
}

func find(es []Entry, raw string) (Entry, bool) {
	for _, e := range es {
		if e.Raw == raw {
			return e, true
		}
	}
	return Entry{}, false
}

func TestParseCheckFixture(t *testing.T) {
	e := newEnv(t)
	th := thread(t, "67494") // Genre: Psychedelic, Harem, Male Protagonist, Vanilla, Group, Yuri, Hand-Holding.
	m, err := e.svc.ParseCheck(ctx, th.GenreText, th.Tags)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		raw, reason, slug, kind string
		group                   []Entry
	}{
		{"Harem", "", "harem", KindF95, m.Exact},
		{"Male Protagonist", "", "male-protagonist", KindF95, m.Exact},
		{"Group", ReasonSynonym, "group-sex", KindF95, m.NeedsLook},
		{"Yuri", ReasonSynonym, "lesbian", KindF95, m.NeedsLook},
		{"Psychedelic", ReasonNoMatch, "psychedelic", KindCustom, m.NeedsLook},
	} {
		en, ok := find(c.group, c.raw)
		if !ok {
			t.Errorf("%s not in its group", c.raw)
			continue
		}
		if en.Reason != c.reason || en.Target.Slug != c.slug || en.Target.Kind != c.kind || !en.Accept || en.Qualifier != QualPresent {
			t.Errorf("%s = %+v", c.raw, en)
		}
	}
	slugs := map[string]bool{}
	for _, f := range m.F95Only {
		if !f.Accept {
			t.Errorf("F95-only %s not accepted by default", f.Slug)
		}
		slugs[f.Slug] = true
	}
	for _, s := range []string{"3dcg", "voyeurism"} {
		if !slugs[s] {
			t.Errorf("%s should be F95-only", s)
		}
	}
	for _, s := range []string{"harem", "group-sex", "lesbian", "male-protagonist"} {
		if slugs[s] {
			t.Errorf("%s is produced by the Genre text, not F95-only", s)
		}
	}
}

func TestParseCheckDefaults(t *testing.T) {
	e := newEnv(t)
	m, err := e.svc.ParseCheck(ctx, "Harem\nTitfuck (optional)\nThis game has lots of lovely things in it (as described) etc.\nPossible: Netorare", nil)
	if err != nil {
		t.Fatal(err)
	}
	if en, ok := find(m.NeedsLook, "Netorare"); !ok || en.Reason != ReasonPossible || en.Qualifier != QualPlanned || !en.Accept {
		t.Errorf("possible = %+v", en)
	}
	prose, ok := find(m.NeedsLook, "This game has lots of lovely things in it (as described) etc")
	if !ok || prose.Reason != ReasonProse || prose.Accept {
		t.Errorf("prose = %+v (%v)", prose, m.NeedsLook)
	}
	if en, ok := find(m.Exact, "Titfuck (optional)"); !ok || en.Qualifier != QualOptional {
		t.Errorf("optional exact = %+v", en)
	}
	if len(m.F95Only) != 0 {
		t.Errorf("no F95 list, no F95-only: %v", m.F95Only)
	}
}

func TestApplyAdd(t *testing.T) {
	e := newEnv(t)
	th := thread(t, "67494")
	g := e.game(t)
	m, err := e.svc.ParseCheck(ctx, th.GenreText, th.Tags)
	if err != nil {
		t.Fatal(err)
	}
	// The user re-points "Yuri" and ignores "Hand-Holding".
	for i := range m.NeedsLook {
		switch m.NeedsLook[i].Raw {
		case "Yuri":
			m.NeedsLook[i].Target = TagRef{Kind: KindF95, Slug: "ntr", Label: "ntr"}
		case "Hand-Holding":
			m.NeedsLook[i].Accept = false
		}
	}
	e.apply(t, g, m)
	got := e.tags(t, g)
	for slug, r := range got {
		if r.Verification != Unverified || r.VerifiedAt.Valid {
			t.Errorf("%s verified at add time: %+v", slug, r)
		}
	}
	for _, c := range []struct {
		slug, origin string
		f95Only      bool
	}{
		{"harem", OriginBoth, false}, {"group-sex", OriginBoth, false}, {"psychedelic", OriginGenre, false},
		{"3dcg", OriginF95List, true}, {"ntr", OriginGenre, false},
	} {
		r, ok := got[c.slug]
		if !ok || r.Origin != c.origin || (r.F95Only == 1) != c.f95Only {
			t.Errorf("%s = %+v (%v)", c.slug, r, ok)
		}
	}
	if r := got["ntr"]; r.MappingOverride != 1 || r.SourcePhrase.String != "Yuri" {
		t.Errorf("remapped tag = %+v", r)
	}
	if _, ok := got["hand-holding"]; ok {
		t.Error("ignored entry was written")
	}
	syn, err := e.svc.ListSynonyms(ctx, "yuri")
	if err != nil || len(syn) != 1 || syn[0].Origin != "user" || syn[0].Tag.Slug != "ntr" {
		t.Errorf("synonym after remap = %+v %v", syn, err)
	}
}

func TestApplyAddGrowsVocabulary(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem", []f95.Tag{{Slug: "brand-new-tag", Label: "brand new tag"}})
	got := e.tags(t, g)
	if r, ok := got["brand-new-tag"]; !ok || r.TagLabel != "brand new tag" || r.TagKind != KindF95 || r.F95Only != 1 {
		t.Errorf("grown tag = %+v", r)
	}
	vocab, _ := e.svc.LoadVocab(ctx)
	if vocab["brandnewtag"] != "brand-new-tag" {
		t.Error("vocabulary did not grow")
	}
}

func TestParseTextHandPicked(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem, Elves", nil)
	if _, err := e.svc.AddByHand(ctx, g, TagRef{Kind: KindCustom, Label: "BBW"}, ""); err != nil {
		t.Fatal(err)
	}
	got := e.tags(t, g)
	if got["harem"].Verification != Unverified || got["elves"].Origin != OriginGenre {
		t.Errorf("pasted tags must be unverified genre: %+v", got)
	}
	if r := got["bbw"]; r.Verification != Confirmed || r.Origin != OriginManual || !r.VerifiedAt.Valid || r.TagKind != KindCustom {
		t.Errorf("hand-picked tag = %+v", r)
	}
}
