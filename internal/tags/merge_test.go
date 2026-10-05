package tags

import "testing"

func TestMergeRefresh(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem, Titfuck, Vore\nPlanned: Pregnancy", list("harem", "vore", "anal-sex"))
	// The user judged Harem and Vore, and added Elves by hand.
	rows := e.tags(t, g)
	if err := e.svc.SetVerification(ctx, rows["harem"].ID, Confirmed); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetVerification(ctx, rows["vore"].ID, Wrong); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddByHand(ctx, g, TagRef{Kind: KindCustom, Label: "Elves"}, ""); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(24 * 3600e9)

	// Refresh: Titfuck and Vore gone from Genre, Vore still listed, anal-sex gone from the list,
	// Pregnancy now present, Creampie new, 3dcg new in the F95 list only.
	res := e.merge(t, g, "Harem, Pregnancy, Creampie", true, list("harem", "vore", "3dcg", "pregnancy"))
	if res.New != 2 || res.Promoted != 1 {
		t.Errorf("result = %+v", res)
	}
	got := e.tags(t, g)
	if r := got["harem"]; r.Verification != Confirmed || r.IsNew != 0 || r.RemovedAtSourceAt.Valid || r.Origin != OriginBoth {
		t.Errorf("harem = %+v", r)
	}
	if r := got["vore"]; r.Verification != Wrong || r.Origin != OriginF95List || r.F95Only != 1 || r.RemovedAtSourceAt.Valid {
		t.Errorf("vore (listed, no longer in Genre) = %+v", r)
	}
	if r := got["titfuck"]; !r.RemovedAtSourceAt.Valid || r.Verification != Unverified {
		t.Errorf("titfuck should be flagged removed: %+v", r)
	}
	if r := got["anal-sex"]; !r.RemovedAtSourceAt.Valid {
		t.Errorf("anal-sex left the F95 list: %+v", r)
	}
	if r := got["pregnancy"]; r.Qualifier != QualPresent || r.Promoted != 1 || r.Origin != OriginBoth {
		t.Errorf("pregnancy should be promoted: %+v", r)
	}
	if r := got["creampie"]; r.IsNew != 1 || r.Verification != Unverified || r.Origin != OriginGenre {
		t.Errorf("creampie = %+v", r)
	}
	if r := got["3dcg"]; r.IsNew != 1 || r.F95Only != 1 || r.Origin != OriginF95List {
		t.Errorf("3dcg = %+v", r)
	}
	if r := got["elves"]; r.RemovedAtSourceAt.Valid || r.Origin != OriginManual || r.Verification != Confirmed {
		t.Errorf("manual tag must survive: %+v", r)
	}

	// Gone tags reappear: the flag clears and the row is reused.
	e.merge(t, g, "Harem, Titfuck", true, list("harem"))
	if r := e.tags(t, g)["titfuck"]; r.RemovedAtSourceAt.Valid || r.IsNew != 0 {
		t.Errorf("returned tag = %+v", r)
	}
}

func TestMergeRefreshWithoutGenreKeepsGenreSide(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem, Titfuck", list("harem"))
	e.merge(t, g, "", false, list("harem", "vore"))
	got := e.tags(t, g)
	if r := got["titfuck"]; r.RemovedAtSourceAt.Valid || r.Origin != OriginGenre {
		t.Errorf("genre tag untouched without Genre text: %+v", r)
	}
	if r := got["harem"]; r.Origin != OriginBoth {
		t.Errorf("harem = %+v", r)
	}
	if r := got["vore"]; r.IsNew != 1 || r.F95Only != 1 {
		t.Errorf("vore = %+v", r)
	}
}

func TestMergeRefreshKeepsMappingOverride(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Yuri", nil) // synonym → lesbian
	row := e.tags(t, g)["lesbian"]
	if _, err := e.svc.SetMapping(ctx, row.ID, TagRef{Kind: KindF95, Slug: "ntr", Label: "ntr"}); err != nil {
		t.Fatal(err)
	}
	// The seed Synonym is gone, so the phrase would map to lesbian again; the override must hold.
	syn, _ := e.svc.ListSynonyms(ctx, "yuri")
	if err := e.svc.DeleteSynonym(ctx, syn[0].ID); err != nil {
		t.Fatal(err)
	}
	res := e.merge(t, g, "Yuri", true, nil)
	got := e.tags(t, g)
	if res.New != 0 || res.Removed != 0 || got["ntr"].RemovedAtSourceAt.Valid || len(got) != 1 {
		t.Errorf("override lost: %+v %+v", res, got)
	}
}

func TestMergeDoesNotDemoteOrDuplicate(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Harem", nil)
	e.merge(t, g, "Planned: Harem", true, nil)
	if r := e.tags(t, g)["harem"]; r.Qualifier != QualPresent || r.Promoted != 0 {
		t.Errorf("present must stay present: %+v", r)
	}
	e.merge(t, g, "Harem, Harem\nPlanned: Harem", true, nil)
	if n := len(e.tags(t, g)); n != 1 {
		t.Errorf("duplicates not collapsed: %d", n)
	}
}

func TestMergeKeepsSiblingOfRemappedSplitPhrase(t *testing.T) {
	e := newEnv(t)
	g := e.game(t)
	e.add(t, g, "Incest/NTR", nil)
	rows := e.tags(t, g)
	if len(rows) != 2 {
		t.Fatalf("split phrase rows = %v", rows)
	}
	if _, err := e.svc.SetMapping(ctx, rows["incest"].ID, TagRef{Kind: KindF95, Slug: "corruption"}); err != nil {
		t.Fatal(err)
	}
	e.merge(t, g, "Incest/NTR", true, nil)
	got := e.tags(t, g)
	if r := got["ntr"]; r.RemovedAtSourceAt.Valid {
		t.Errorf("sibling ntr removed: %+v", r)
	}
	if r := got["corruption"]; r.RemovedAtSourceAt.Valid || r.MappingOverride != 1 {
		t.Errorf("corruption = %+v", r)
	}
	if _, ok := got["incest"]; ok {
		t.Errorf("override re-created incest")
	}
}
