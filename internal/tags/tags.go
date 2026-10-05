// Package tags is the Game tag service: vocabulary, Synonyms, the add-time
// parse check, merge on refresh, hand edits and the Tag review (R-TAG-1..13).
package tags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/genre"
)

// Service owns the tag, synonym, game_tag and tag_review tables. Methods with a
// Tx suffix run inside the caller's transaction and never open their own.
type Service struct {
	store *db.Store
	clock clock.Clock
}

func New(store *db.Store, clk clock.Clock) *Service { return &Service{store: store, clock: clk} }

func (s *Service) now() string { return clock.Timestamp(s.clock.Now()) }

// Tag kinds, origins, qualifiers and verifications as stored.
const (
	KindF95    = "f95"
	KindCustom = "custom"

	OriginF95List = "f95_list"
	OriginGenre   = "genre"
	OriginBoth    = "both"
	OriginManual  = "manual"

	QualPresent  = "present"
	QualPlanned  = "planned"
	QualOptional = "optional"

	Unverified = "unverified"
	Confirmed  = "confirmed"
	Wrong      = "wrong"
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrValidation, fmt.Sprintf(format, a...))
}

func notFound(what string, id int64) error {
	return fmt.Errorf("%w: %s %d", domain.ErrNotFound, what, id)
}

func mapNoRows(err error, what string, id int64) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(what, id)
	}
	return err
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// TagRef names a tag. Custom tags are keyed by genre.Key(Label); an F95 tag by its slug.
type TagRef struct {
	Kind  string `json:"kind"`
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

func (r TagRef) custom() bool { return r.Kind == KindCustom }

func refOf(t sqlcgen.Tag) TagRef { return TagRef{Kind: t.Kind, Slug: t.Slug, Label: t.Label} }

// EnsureTagTx returns the tag for ref, creating it when missing. Tags are never deleted.
func (s *Service) EnsureTagTx(ctx context.Context, q *sqlcgen.Queries, ref TagRef) (sqlcgen.Tag, error) {
	switch ref.Kind {
	case KindF95:
		if ref.Slug == "" {
			return sqlcgen.Tag{}, invalid("f95 tag needs a slug")
		}
		if ref.Label == "" {
			ref.Label = ref.Slug
		}
	case KindCustom:
		ref.Label = strings.TrimSpace(ref.Label)
		ref.Slug = genre.Key(ref.Label)
		if ref.Slug == "" {
			return sqlcgen.Tag{}, invalid("custom tag needs a name with letters or digits")
		}
	default:
		return sqlcgen.Tag{}, invalid("tag kind %q", ref.Kind)
	}
	return q.UpsertTag(ctx, sqlcgen.UpsertTagParams{Kind: ref.Kind, Slug: ref.Slug, Label: ref.Label})
}

// GrowVocabularyTx inserts every F95 tag of a logged-in tag list that the
// vocabulary does not know yet (R-TAG-1, INV-20); existing labels are kept.
func (s *Service) GrowVocabularyTx(ctx context.Context, q *sqlcgen.Queries, list []f95.Tag) error {
	for _, t := range list {
		if t.Slug == "" {
			continue
		}
		if _, err := s.EnsureTagTx(ctx, q, TagRef{Kind: KindF95, Slug: t.Slug, Label: t.Label}); err != nil {
			return err
		}
	}
	return nil
}

// vocab is Key(slug) → slug over the F95 tags, plus slug → label for display.
func loadVocab(ctx context.Context, q *sqlcgen.Queries) (map[string]string, map[string]string, error) {
	rows, err := q.ListTagsByKind(ctx, KindF95)
	if err != nil {
		return nil, nil, err
	}
	slugs := make([]string, len(rows))
	labels := make(map[string]string, len(rows))
	for i, r := range rows {
		slugs[i] = r.Slug
		labels[r.Slug] = r.Label
	}
	return genre.Vocabulary(slugs), labels, nil
}

func loadSynonyms(ctx context.Context, q *sqlcgen.Queries) (map[string]genre.Target, error) {
	rows, err := q.ListSynonymRows(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]genre.Target, len(rows))
	for _, r := range rows {
		out[r.PhraseKey] = targetOf(TagRef{Kind: r.TagKind, Slug: r.TagSlug, Label: r.TagLabel})
	}
	return out, nil
}

func targetOf(r TagRef) genre.Target {
	if r.custom() {
		return genre.Target{Name: r.Label, Custom: true}
	}
	return genre.Target{Name: r.Slug}
}

// LoadVocab returns the F95 vocabulary (Key(slug) → slug) for genre.Matches.
func (s *Service) LoadVocab(ctx context.Context) (map[string]string, error) {
	v, _, err := loadVocab(ctx, s.store.Queries())
	return v, err
}

// LoadSynonyms returns the Synonym table (phrase key → target) for genre.Matches.
func (s *Service) LoadSynonyms(ctx context.Context) (map[string]genre.Target, error) {
	return loadSynonyms(ctx, s.store.Queries())
}

// Tags lists the whole vocabulary (F95 and Custom tags) for pickers.
func (s *Service) Tags(ctx context.Context) ([]TagRef, error) {
	rows, err := s.store.Queries().ListTags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TagRef, len(rows))
	for i, r := range rows {
		out[i] = refOf(r)
	}
	return out, nil
}

// GameTags lists a Game's tags with their tag rows.
func (s *Service) GameTags(ctx context.Context, gameID int64) ([]sqlcgen.ListGameTagsRow, error) {
	return s.store.Queries().ListGameTags(ctx, gameID)
}

// resolveTx maps a genre target to its tag row, creating custom tags.
func (s *Service) resolveTx(ctx context.Context, q *sqlcgen.Queries, t genre.Target) (sqlcgen.Tag, error) {
	if t.Custom {
		return s.EnsureTagTx(ctx, q, TagRef{Kind: KindCustom, Label: t.Name})
	}
	tag, err := q.GetTagByKindSlug(ctx, sqlcgen.GetTagByKindSlugParams{Kind: KindF95, Slug: t.Name})
	if err != nil {
		return tag, fmt.Errorf("tag %q: %w", t.Name, err)
	}
	return tag, nil
}

func rank(q string) int {
	switch q {
	case QualPresent:
		return 2
	case QualOptional:
		return 1
	}
	return 0
}

func validQualifier(q string) bool { return q == QualPresent || q == QualPlanned || q == QualOptional }

// mergeOrigin combines two origins of the same Game tag; manual always wins.
func mergeOrigin(a, b string) string {
	switch {
	case a == OriginManual || b == OriginManual:
		return OriginManual
	case a == b:
		return a
	}
	return OriginBoth
}

func hasGenre(origin string) bool   { return origin == OriginGenre || origin == OriginBoth }
func hasF95List(origin string) bool { return origin == OriginF95List || origin == OriginBoth }

func originOf(inGenre, inF95 bool) string {
	switch {
	case inGenre && inF95:
		return OriginBoth
	case inGenre:
		return OriginGenre
	}
	return OriginF95List
}

func insertParams(r sqlcgen.GameTag) sqlcgen.InsertGameTagParams {
	return sqlcgen.InsertGameTagParams{
		GameID: r.GameID, TagID: r.TagID, Origin: r.Origin, Qualifier: r.Qualifier, Verification: r.Verification,
		ModifierNote: r.ModifierNote, SourcePhrase: r.SourcePhrase, MappingOverride: r.MappingOverride, IsNew: r.IsNew,
		Promoted: r.Promoted, RemovedAtSourceAt: r.RemovedAtSourceAt, F95Only: r.F95Only, VerifiedAt: r.VerifiedAt,
	}
}

func updateParams(r sqlcgen.GameTag) sqlcgen.UpdateGameTagParams {
	return sqlcgen.UpdateGameTagParams{
		ID: r.ID, TagID: r.TagID, Origin: r.Origin, Qualifier: r.Qualifier, Verification: r.Verification,
		ModifierNote: r.ModifierNote, SourcePhrase: r.SourcePhrase, MappingOverride: r.MappingOverride, IsNew: r.IsNew,
		Promoted: r.Promoted, RemovedAtSourceAt: r.RemovedAtSourceAt, F95Only: r.F95Only, VerifiedAt: r.VerifiedAt,
	}
}

// foldInto merges src (a row about to disappear) into dst, the Game's existing
// row for the same tag (R-TAG-4, INV-13): strongest qualifier, combined origin,
// dst's verification kept unless dst is unverified and src is not.
func foldInto(dst, src sqlcgen.GameTag) sqlcgen.GameTag {
	if rank(src.Qualifier) > rank(dst.Qualifier) {
		dst.Qualifier = src.Qualifier
	}
	dst.Origin = mergeOrigin(dst.Origin, src.Origin)
	if dst.Origin != OriginManual {
		dst.F95Only = b2i(dst.Origin == OriginF95List)
	}
	if !dst.SourcePhrase.Valid {
		dst.SourcePhrase = src.SourcePhrase
	}
	if !dst.ModifierNote.Valid {
		dst.ModifierNote = src.ModifierNote
	}
	if dst.Verification == Unverified && src.Verification != Unverified {
		dst.Verification, dst.VerifiedAt = src.Verification, src.VerifiedAt
	}
	dst.MappingOverride |= src.MappingOverride
	dst.IsNew |= src.IsNew
	dst.Promoted |= src.Promoted
	if dst.RemovedAtSourceAt.Valid && !src.RemovedAtSourceAt.Valid {
		dst.RemovedAtSourceAt = sql.NullString{}
	}
	return dst
}

// repointTx moves row to newTag. When the Game already has a row for newTag the
// two collapse into that row and row is deleted. A row that was both Genre and
// F95 list leaves an f95_list row behind for its old tag, since the F95 list
// still carries it. It returns whether a collapse happened.
func repointTx(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.GameTag, newTag int64, override bool) (collapsed bool, err error) {
	if row.TagID == newTag {
		if override && row.MappingOverride == 0 {
			row.MappingOverride = 1
			return false, q.UpdateGameTag(ctx, updateParams(row))
		}
		return false, nil
	}
	oldTag, wasBoth := row.TagID, row.Origin == OriginBoth
	if wasBoth {
		row.Origin = OriginGenre
	}
	if override {
		row.MappingOverride = 1
	}
	existing, err := q.GetGameTagByTag(ctx, sqlcgen.GetGameTagByTagParams{GameID: row.GameID, TagID: newTag})
	switch {
	case err == nil:
		moved := row
		moved.TagID = newTag
		if err := q.UpdateGameTag(ctx, updateParams(foldInto(existing, moved))); err != nil {
			return false, err
		}
		collapsed, err = true, q.DeleteGameTag(ctx, row.ID)
	case errors.Is(err, sql.ErrNoRows):
		row.TagID = newTag
		err = q.UpdateGameTag(ctx, updateParams(row))
	}
	if err != nil || !wasBoth {
		return collapsed, err
	}
	left := sqlcgen.GameTag{
		GameID: row.GameID, TagID: oldTag, Origin: OriginF95List, Qualifier: QualPresent,
		Verification: Unverified, F95Only: 1,
	}
	_, err = q.InsertGameTag(ctx, insertParams(left))
	return collapsed, err
}

func gameTagOf(r sqlcgen.ListGameTagsRow) sqlcgen.GameTag {
	return sqlcgen.GameTag{
		ID: r.ID, GameID: r.GameID, TagID: r.TagID, Origin: r.Origin, Qualifier: r.Qualifier, Verification: r.Verification,
		ModifierNote: r.ModifierNote, SourcePhrase: r.SourcePhrase, MappingOverride: r.MappingOverride, IsNew: r.IsNew,
		Promoted: r.Promoted, RemovedAtSourceAt: r.RemovedAtSourceAt, F95Only: r.F95Only, VerifiedAt: r.VerifiedAt,
	}
}
