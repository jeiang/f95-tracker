package tags

import (
	"errors"
	"testing"

	"github.com/jeiang/f95-tracker/internal/domain"
)

func TestSynonymReapply(t *testing.T) {
	e := newEnv(t)
	// "Threesome" has no tag yet: Custom tag on every Game that wrote it.
	unverified, confirmed, overridden, dup := e.game(t), e.game(t), e.game(t), e.game(t)
	for _, g := range []int64{unverified, confirmed, overridden, dup} {
		e.add(t, g, "Threesome", nil)
	}
	if err := e.svc.SetVerification(ctx, e.tags(t, confirmed)["threesome"].ID, Confirmed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetMapping(ctx, e.tags(t, overridden)["threesome"].ID, TagRef{Kind: KindF95, Slug: "harem"}); err != nil {
		t.Fatal(err)
	}
	// The mapping fix above saved a user Synonym threesome → harem and re-applied it to the others.
	syn, _ := e.svc.ListSynonyms(ctx, "threesome")
	if len(syn) != 1 || syn[0].Tag.Slug != "harem" {
		t.Fatalf("synonyms = %+v", syn)
	}
	if _, ok := e.tags(t, unverified)["harem"]; !ok {
		t.Fatal("unverified tag was not re-pointed by the fix")
	}
	// dup also has Group sex by hand: pointing the Synonym there collapses the rows.
	if _, err := e.svc.AddByHand(ctx, dup, TagRef{Kind: KindF95, Slug: "group-sex"}, QualPlanned); err != nil {
		t.Fatal(err)
	}
	_, res, err := e.svc.UpdateSynonym(ctx, syn[0].ID, "Three some", TagRef{Kind: KindF95, Slug: "group-sex", Label: "group sex"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Repointed != 2 || res.Collapsed != 1 {
		t.Errorf("reapply = %+v", res)
	}
	if got := e.tags(t, unverified); got["group-sex"].Verification != Unverified || len(got) != 1 {
		t.Errorf("unverified = %+v", got)
	}
	if got := e.tags(t, confirmed); got["threesome"].Verification != Confirmed || len(got) != 1 {
		t.Errorf("confirmed row must stay on its tag: %+v", got)
	}
	if got := e.tags(t, overridden); got["harem"].MappingOverride != 1 || len(got) != 1 {
		t.Errorf("override must stay: %+v", got)
	}
	got := e.tags(t, dup)
	if r := got["group-sex"]; len(got) != 1 || r.Verification != Confirmed || r.Qualifier != QualPresent {
		t.Errorf("collapsed row = %+v", got)
	}
}

func TestSynonymCRUD(t *testing.T) {
	e := newEnv(t)
	row, _, err := e.svc.CreateSynonym(ctx, "Big Booty", TagRef{Kind: KindF95, Slug: "big-ass"})
	if err != nil || row.PhraseKey != "bigbooty" || row.Origin != "user" {
		t.Fatalf("create = %+v %v", row, err)
	}
	if _, _, err := e.svc.CreateSynonym(ctx, "bigbooty", TagRef{Kind: KindF95, Slug: "harem"}); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate key err = %v", err)
	}
	if _, _, err := e.svc.CreateSynonym(ctx, "!!!", TagRef{Kind: KindF95, Slug: "harem"}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("empty key err = %v", err)
	}
	if rows, _ := e.svc.ListSynonyms(ctx, "big-ass"); len(rows) != 1 {
		t.Errorf("filter by target = %v", rows)
	}
	if rows, _ := e.svc.ListSynonyms(ctx, "nomatchhere"); len(rows) != 0 {
		t.Errorf("filter = %v", rows)
	}
	syn, _ := e.svc.LoadSynonyms(ctx)
	if syn["bigbooty"].Name != "big-ass" {
		t.Errorf("loader = %+v", syn["bigbooty"])
	}
	if err := e.svc.DeleteSynonym(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteSynonym(ctx, row.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete twice err = %v", err)
	}
	if n, _ := e.store.Queries().CountSynonymsByOrigin(ctx, "seed"); n == 0 {
		t.Error("seed synonyms must remain")
	}
}
