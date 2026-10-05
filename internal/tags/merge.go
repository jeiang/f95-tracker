package tags

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/genre"
)

// MergeResult counts what a refresh merge changed.
type MergeResult struct {
	New      int // tags that arrived (unverified, is_new)
	Removed  int // tags newly flagged removed at source
	Promoted int // planned/optional tags promoted to present
}

type observed struct {
	inGenre, inF95 bool
	qualifier      string
	raw, note      string
}

// MergeRefreshTx merges a logged-in detail fetch into the Game's tags (R-TAG-7,
// INV-16). Verifications and mapping overrides persist; new tags arrive
// unverified with is_new; tags gone from the Source keep their row with
// removed_at_source_at; a planned/optional tag that now reads present (Genre or
// F95 list) is promoted and flagged; f95_only is recomputed; origin 'manual'
// rows are never touched. f95Tags is the authoritative logged-in list and grows
// the vocabulary. Without Genre text (hasGenre false) the Genre side is left as
// stored rather than treated as empty.
func (s *Service) MergeRefreshTx(ctx context.Context, q *sqlcgen.Queries, gameID int64, genreText string, hasGenre bool, f95Tags []f95.Tag) (MergeResult, error) {
	var res MergeResult
	if err := s.GrowVocabularyTx(ctx, q, f95Tags); err != nil {
		return res, err
	}
	rows, err := q.ListGameTags(ctx, gameID)
	if err != nil {
		return res, err
	}
	existing := make(map[int64]sqlcgen.GameTag, len(rows))
	overrides := map[string]int64{} // source phrase → tag id of a user-remapped row
	type phraseTag struct {
		phrase string
		tag    int64
	}
	phraseRow := map[phraseTag]bool{} // rows already carrying a phrase: siblings of a split phrase
	for _, r := range rows {
		existing[r.TagID] = gameTagOf(r)
		if r.SourcePhrase.Valid {
			phraseRow[phraseTag{r.SourcePhrase.String, r.TagID}] = true
		}
		if r.MappingOverride == 1 && r.SourcePhrase.Valid {
			overrides[r.SourcePhrase.String] = r.TagID
		}
	}

	obs := map[int64]*observed{}
	order := []int64{}
	get := func(id int64) *observed {
		o := obs[id]
		if o == nil {
			o = &observed{}
			obs[id] = o
			order = append(order, id)
		}
		return o
	}
	overrideSeen := map[int64]bool{}
	if hasGenre {
		vocab, _, err := loadVocab(ctx, q)
		if err != nil {
			return res, err
		}
		syn, err := loadSynonyms(ctx, q)
		if err != nil {
			return res, err
		}
		items := genre.Parse(genreText)
		for _, it := range items {
			if id, ok := overrides[it.Raw]; ok {
				overrideSeen[id] = true
			}
		}
		for _, m := range genre.Matches(items, vocab, syn) {
			if m.Kind == genre.Prose || (m.Target.Custom && genre.Key(m.Target.Name) == "") {
				continue
			}
			tag, err := s.resolveTx(ctx, q, m.Target)
			if err != nil {
				return res, err
			}
			if id, ok := overrides[m.Raw]; ok && id != tag.ID && !phraseRow[phraseTag{m.Raw, tag.ID}] {
				continue
			}
			o := get(tag.ID)
			o.inGenre, o.qualifier, o.raw = true, string(m.Qualifier), m.Raw
			o.note = joinNote(m)
		}
	}
	for _, t := range f95Tags {
		if t.Slug == "" {
			continue
		}
		tag, err := q.GetTagByKindSlug(ctx, sqlcgen.GetTagByKindSlugParams{Kind: KindF95, Slug: t.Slug})
		if err != nil {
			return res, err
		}
		get(tag.ID).inF95 = true
	}

	now := s.now()
	for _, id := range order {
		if _, ok := existing[id]; ok {
			continue
		}
		o := obs[id]
		nr := sqlcgen.GameTag{
			GameID: gameID, TagID: id, Origin: originOf(o.inGenre, o.inF95), Qualifier: QualPresent,
			Verification: Unverified, IsNew: 1, F95Only: b2i(o.inF95 && !o.inGenre),
		}
		if o.inGenre {
			nr.Qualifier, nr.SourcePhrase, nr.ModifierNote = o.qualifier, nullStr(o.raw), nullStr(o.note)
		}
		if _, err := q.InsertGameTag(ctx, insertParams(nr)); err != nil {
			return res, err
		}
		res.New++
	}
	for id, row := range existing {
		if row.Origin == OriginManual {
			continue
		}
		orig := row
		gone, promoted := applyObserved(&row, obs[id], overrideSeen[id], hasGenre)
		if gone && !row.RemovedAtSourceAt.Valid {
			row.RemovedAtSourceAt = nullStr(now)
			res.Removed++
		}
		if promoted {
			res.Promoted++
		}
		if row != orig {
			if err := q.UpdateGameTag(ctx, updateParams(row)); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func joinNote(m genre.Match) string { return strings.Join(nonEmptyStrings(m.Modifier, m.Note), "; ") }

// applyObserved updates an existing, non-manual row for what the fetch saw
// (o nil: neither the F95 list nor the Genre text produced the tag). gone means
// nothing carries the tag any more and row is left as it was.
func applyObserved(row *sqlcgen.GameTag, o *observed, overrideSeen, hasGenreText bool) (gone, promoted bool) {
	seenF95 := o != nil && o.inF95
	seenGenre := overrideSeen || (o != nil && o.inGenre)
	if !hasGenreText {
		seenGenre = hasGenre(row.Origin)
	}
	if !seenGenre && !seenF95 {
		return true, false
	}
	newlyF95 := seenF95 && !hasF95List(row.Origin)
	row.RemovedAtSourceAt = sql.NullString{}
	row.Origin = originOf(seenGenre, seenF95)
	row.F95Only = b2i(seenF95 && !seenGenre)
	if row.Qualifier != QualPresent {
		genrePresent := o != nil && o.inGenre && o.qualifier == QualPresent
		if genrePresent || newlyF95 {
			row.Qualifier, row.Promoted, promoted = QualPresent, 1, true
		}
	}
	if o != nil && o.inGenre {
		if !row.SourcePhrase.Valid {
			row.SourcePhrase = nullStr(o.raw)
		}
		if !row.ModifierNote.Valid {
			row.ModifierNote = nullStr(o.note)
		}
	}
	return false, promoted
}
