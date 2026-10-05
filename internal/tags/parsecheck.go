package tags

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/genre"
)

// Reasons an Entry sits under "Needs a look" (R-TAG-13).
const (
	ReasonSynonym  = "synonym"
	ReasonNoMatch  = "no_match"
	ReasonProse    = "prose"
	ReasonPossible = "possible"
)

// Entry is one Genre phrase of the parse check. Target, Qualifier and Accept
// are what the user edits; Suggested is what the parser proposed.
type Entry struct {
	Raw       string `json:"raw"`
	PhraseKey string `json:"phrase_key,omitempty"` // Synonym key a changed mapping is saved under; "" = never saved
	Reason    string `json:"reason,omitempty"`     // "" for exact matches
	Suggested TagRef `json:"suggested"`            // zero for prose
	Target    TagRef `json:"target"`               // zero = no tag chosen
	Qualifier string `json:"qualifier"`
	Note      string `json:"note,omitempty"` // modifier word and parentheticals → modifier_note
	Accept    bool   `json:"accept"`
}

// F95OnlyEntry is a tag of the F95 list that the Genre text does not produce.
type F95OnlyEntry struct {
	Slug   string `json:"slug"`
	Label  string `json:"label"`
	Accept bool   `json:"accept"`
}

// ParseCheckModel is the data behind the Add Game page (R-TAG-13) and, after
// the user's edits, the decisions ApplyAddTx writes.
type ParseCheckModel struct {
	Exact     []Entry        `json:"exact"`
	NeedsLook []Entry        `json:"needs_look"`
	F95Only   []F95OnlyEntry `json:"f95_only"`
	F95List   []f95.Tag      `json:"f95_list"` // the logged-in tag list this was built from
}

// ParseCheck runs the Genre text through the parser against the current
// vocabulary (extended by the thread's own tag list) and Synonyms, and sets the
// R-TAG-13 Confirm defaults: exact, synonym, no-match (Custom tag) and possible
// (planned) entries accepted; prose ignored; F95-only tags accepted.
// With no f95Tags it is the pasted-text prefill of manual and itch.io Games (R-TAG-12).
func (s *Service) ParseCheck(ctx context.Context, genreText string, f95Tags []f95.Tag) (ParseCheckModel, error) {
	q := s.store.Queries()
	vocab, labels, err := loadVocab(ctx, q)
	if err != nil {
		return ParseCheckModel{}, err
	}
	syn, err := loadSynonyms(ctx, q)
	if err != nil {
		return ParseCheckModel{}, err
	}
	slugs := make([]string, 0, len(f95Tags))
	for _, t := range f95Tags {
		if t.Slug == "" {
			continue
		}
		if _, ok := labels[t.Slug]; !ok {
			vocab[genre.Key(t.Slug)] = t.Slug
			labels[t.Slug] = nonEmpty(t.Label, t.Slug)
		}
		slugs = append(slugs, t.Slug)
	}
	items := genre.Parse(genreText)
	byRaw := make(map[string]genre.Item, len(items))
	for _, it := range items {
		if _, ok := byRaw[it.Raw]; !ok {
			byRaw[it.Raw] = it
		}
	}
	matches := genre.Matches(items, vocab, syn)

	m := ParseCheckModel{Exact: []Entry{}, NeedsLook: []Entry{}, F95Only: []F95OnlyEntry{}, F95List: f95Tags}
	for _, mt := range matches {
		e := entryOf(mt, byRaw[mt.Raw], labels, syn)
		if mt.NeedsLook() {
			m.NeedsLook = append(m.NeedsLook, e)
		} else {
			m.Exact = append(m.Exact, e)
		}
	}
	for _, slug := range genre.F95Only(matches, slugs) {
		m.F95Only = append(m.F95Only, F95OnlyEntry{Slug: slug, Label: labels[slug], Accept: true})
	}
	return m, nil
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func entryOf(mt genre.Match, it genre.Item, labels map[string]string, syn map[string]genre.Target) Entry {
	e := Entry{Raw: mt.Raw, Qualifier: string(mt.Qualifier), Accept: mt.Kind != genre.Prose}
	e.Note = strings.Join(nonEmptyStrings(mt.Modifier, mt.Note), "; ")
	switch {
	case mt.Kind == genre.Prose:
		e.Reason = ReasonProse
	case mt.Possible:
		e.Reason = ReasonPossible
	case mt.Kind == genre.NoMatch:
		e.Reason = ReasonNoMatch
	case mt.Kind == genre.Synonym:
		e.Reason = ReasonSynonym
	}
	if mt.Kind != genre.Prose {
		if mt.Target.Custom {
			e.Suggested = TagRef{Kind: KindCustom, Slug: genre.Key(mt.Target.Name), Label: mt.Target.Name}
		} else {
			e.Suggested = TagRef{Kind: KindF95, Slug: mt.Target.Name, Label: labels[mt.Target.Name]}
		}
		e.Target = e.Suggested
	}
	// A Synonym is saved under the whole phrase's key; a modifier or "a/b" derived match has none.
	key := genre.Key(it.Phrase)
	_, inSyn := syn[key]
	if key != "" && mt.Kind != genre.Prose && mt.Kind != genre.Exact && (mt.Kind == genre.NoMatch || inSyn) {
		e.PhraseKey = key
	}
	return e
}

func nonEmptyStrings(in ...string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParseText is the R-TAG-12 prefill for manual and itch.io Games: pasted tag text, no F95 list.
func (s *Service) ParseText(ctx context.Context, text string) (ParseCheckModel, error) {
	return s.ParseCheck(ctx, text, nil)
}

// ApplyAddTx writes the Game tags of a confirmed parse check (R-TAG-13, R-TAG-6).
// Every tag is stored unverified (add-time confirmation verifies nothing, INV-15);
// origin is genre, or both when the tag is also in the F95 list; accepted F95-only
// tags are origin f95_list with f95_only. A changed mapping of a phrase is saved
// as a user Synonym. The thread's tag list also grows the vocabulary.
func (s *Service) ApplyAddTx(ctx context.Context, q *sqlcgen.Queries, gameID int64, d ParseCheckModel) error {
	if err := s.GrowVocabularyTx(ctx, q, d.F95List); err != nil {
		return err
	}
	inList := make(map[string]bool, len(d.F95List))
	for _, t := range d.F95List {
		inList[t.Slug] = true
	}
	for _, e := range append(append([]Entry{}, d.Exact...), d.NeedsLook...) {
		if !e.Accept || (e.Target.Slug == "" && e.Target.Label == "") {
			continue
		}
		if !validQualifier(e.Qualifier) {
			return invalid("qualifier %q of %q", e.Qualifier, e.Raw)
		}
		tag, err := s.EnsureTagTx(ctx, q, e.Target)
		if err != nil {
			return err
		}
		remapped := e.PhraseKey != "" && (tag.Kind != e.Suggested.Kind || tag.Slug != e.Suggested.Slug)
		if remapped {
			if _, err := s.saveUserSynonymTx(ctx, q, e.PhraseKey, tag.ID); err != nil {
				return err
			}
		}
		row := sqlcgen.GameTag{
			GameID: gameID, TagID: tag.ID, Qualifier: e.Qualifier, Verification: Unverified,
			Origin:       originOf(true, tag.Kind == KindF95 && inList[tag.Slug]),
			SourcePhrase: nullStr(e.Raw), ModifierNote: nullStr(e.Note), MappingOverride: b2i(remapped),
		}
		if err := addOrFoldTx(ctx, q, row); err != nil {
			return err
		}
	}
	for _, e := range d.F95Only {
		if !e.Accept {
			continue
		}
		tag, err := s.EnsureTagTx(ctx, q, TagRef{Kind: KindF95, Slug: e.Slug, Label: e.Label})
		if err != nil {
			return err
		}
		row := sqlcgen.GameTag{
			GameID: gameID, TagID: tag.ID, Qualifier: QualPresent, Verification: Unverified, Origin: OriginF95List, F95Only: 1,
		}
		if err := addOrFoldTx(ctx, q, row); err != nil {
			return err
		}
	}
	return nil
}

// addOrFoldTx inserts row, or folds it into the Game's existing row for the tag.
func addOrFoldTx(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.GameTag) error {
	existing, err := q.GetGameTagByTag(ctx, sqlcgen.GetGameTagByTagParams{GameID: row.GameID, TagID: row.TagID})
	if errors.Is(err, sql.ErrNoRows) {
		_, err = q.InsertGameTag(ctx, insertParams(row))
		return err
	}
	if err != nil {
		return err
	}
	return q.UpdateGameTag(ctx, updateParams(foldInto(existing, row)))
}
