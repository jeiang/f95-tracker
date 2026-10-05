package tags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/genre"
)

// SynonymRow is one Synonym with its target tag.
type SynonymRow struct {
	ID        int64
	PhraseKey string
	Tag       TagRef
	Origin    string // seed or user
	CreatedAt string
}

// ReapplyResult counts what re-applying a Synonym did to existing Games (R-TAG-10).
type ReapplyResult struct {
	Repointed int // Game tags moved to a different tag
	Collapsed int // of those, rows folded into a tag the Game already had
}

// ListSynonyms returns the Synonym table, optionally filtered by a substring of
// the phrase key or the target's slug or label (R-SET-3).
func (s *Service) ListSynonyms(ctx context.Context, filter string) ([]SynonymRow, error) {
	rows, err := s.store.Queries().ListSynonymRows(ctx, strings.ToLower(strings.TrimSpace(filter)))
	if err != nil {
		return nil, err
	}
	out := make([]SynonymRow, len(rows))
	for i, r := range rows {
		out[i] = SynonymRow{ID: r.ID, PhraseKey: r.PhraseKey, Origin: r.Origin, CreatedAt: r.CreatedAt,
			Tag: TagRef{Kind: r.TagKind, Slug: r.TagSlug, Label: r.TagLabel}}
	}
	return out, nil
}

func phraseKey(phrase string) (string, error) {
	k := genre.Key(phrase)
	if k == "" {
		return "", invalid("phrase needs letters or digits")
	}
	return k, nil
}

// CreateSynonym adds a user Synonym and re-applies it to existing Games.
func (s *Service) CreateSynonym(ctx context.Context, phrase string, target TagRef) (SynonymRow, ReapplyResult, error) {
	key, err := phraseKey(phrase)
	if err != nil {
		return SynonymRow{}, ReapplyResult{}, err
	}
	var res ReapplyResult
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetSynonymByKey(ctx, key); err == nil {
			return fmt.Errorf("%w: synonym %q exists", domain.ErrConflict, key)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tag, err := s.EnsureTagTx(ctx, q, target)
		if err != nil {
			return err
		}
		if _, err := q.InsertSynonym(ctx, sqlcgen.InsertSynonymParams{PhraseKey: key, TagID: tag.ID, Origin: "user", CreatedAt: s.now()}); err != nil {
			return err
		}
		res, err = s.reapplyTx(ctx, q, key)
		return err
	})
	if err != nil {
		return SynonymRow{}, res, err
	}
	return s.synonymByKey(ctx, key), res, nil
}

// UpdateSynonym changes a Synonym's phrase and target (it becomes a user
// Synonym) and re-applies it to existing Games.
func (s *Service) UpdateSynonym(ctx context.Context, id int64, phrase string, target TagRef) (SynonymRow, ReapplyResult, error) {
	key, err := phraseKey(phrase)
	if err != nil {
		return SynonymRow{}, ReapplyResult{}, err
	}
	var res ReapplyResult
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetSynonym(ctx, id); err != nil {
			return mapNoRows(err, "synonym", id)
		}
		if other, err := q.GetSynonymByKey(ctx, key); err == nil && other.ID != id {
			return fmt.Errorf("%w: synonym %q exists", domain.ErrConflict, key)
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tag, err := s.EnsureTagTx(ctx, q, target)
		if err != nil {
			return err
		}
		if _, err := q.UpdateSynonym(ctx, sqlcgen.UpdateSynonymParams{PhraseKey: key, TagID: tag.ID, ID: id}); err != nil {
			return err
		}
		res, err = s.reapplyTx(ctx, q, key)
		return err
	})
	if err != nil {
		return SynonymRow{}, res, err
	}
	return s.synonymByKey(ctx, key), res, nil
}

// DeleteSynonym removes a Synonym. Existing Game tags are left as they are.
func (s *Service) DeleteSynonym(ctx context.Context, id int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetSynonym(ctx, id); err != nil {
			return mapNoRows(err, "synonym", id)
		}
		return q.DeleteSynonym(ctx, id)
	})
}

func (s *Service) synonymByKey(ctx context.Context, key string) SynonymRow {
	rows, _ := s.ListSynonyms(ctx, key)
	for _, r := range rows {
		if r.PhraseKey == key {
			return r
		}
	}
	return SynonymRow{}
}

// saveUserSynonymTx points key at tagID (creating or updating the Synonym as a
// user one) and re-applies it.
func (s *Service) saveUserSynonymTx(ctx context.Context, q *sqlcgen.Queries, key string, tagID int64) (ReapplyResult, error) {
	cur, err := q.GetSynonymByKey(ctx, key)
	switch {
	case err == nil:
		if cur.TagID == tagID && cur.Origin == "user" {
			return ReapplyResult{}, nil
		}
		_, err = q.UpdateSynonym(ctx, sqlcgen.UpdateSynonymParams{PhraseKey: key, TagID: tagID, ID: cur.ID})
	case errors.Is(err, sql.ErrNoRows):
		_, err = q.InsertSynonym(ctx, sqlcgen.InsertSynonymParams{PhraseKey: key, TagID: tagID, Origin: "user", CreatedAt: s.now()})
	}
	if err != nil {
		return ReapplyResult{}, err
	}
	return s.reapplyTx(ctx, q, key)
}

// reapplyTx re-parses the source phrase of every unverified, non-override Game
// tag and re-points those the Synonym key now maps elsewhere (INV-19). A phrase
// is affected only if resolving it with and without the key differs.
func (s *Service) reapplyTx(ctx context.Context, q *sqlcgen.Queries, key string) (ReapplyResult, error) {
	var res ReapplyResult
	vocab, _, err := loadVocab(ctx, q)
	if err != nil {
		return res, err
	}
	after, err := loadSynonyms(ctx, q)
	if err != nil {
		return res, err
	}
	without := maps.Clone(after)
	delete(without, key)
	cands, err := q.ListReapplyCandidates(ctx)
	if err != nil {
		return res, err
	}
	type group struct {
		game   int64
		phrase string
	}
	groups := map[group][]sqlcgen.GameTag{}
	var order []group
	for _, c := range cands {
		g := group{c.GameID, c.SourcePhrase.String}
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], c)
	}
	for _, g := range order {
		items := genre.Parse(g.phrase)
		mAfter := genre.Matches(items, vocab, after)
		if slices.Equal(targetKeys(mAfter), targetKeys(genre.Matches(items, vocab, without))) {
			continue
		}
		var newIDs []int64
		inAfter := map[int64]bool{}
		for _, m := range mAfter {
			if m.Kind == genre.Prose {
				continue
			}
			tag, err := s.resolveTx(ctx, q, m.Target)
			if err != nil {
				return res, err
			}
			newIDs = append(newIDs, tag.ID)
			inAfter[tag.ID] = true
		}
		if len(newIDs) == 0 {
			continue
		}
		have := map[int64]bool{}
		for _, r := range groups[g] {
			have[r.TagID] = true
		}
		var free []int64
		for _, id := range newIDs {
			if !have[id] {
				free = append(free, id)
			}
		}
		for _, r := range groups[g] {
			if inAfter[r.TagID] {
				continue
			}
			target := newIDs[0]
			if len(free) > 0 {
				target, free = free[0], free[1:]
			}
			collapsed, err := repointTx(ctx, q, r, target, false)
			if err != nil {
				return res, err
			}
			res.Repointed++
			if collapsed {
				res.Collapsed++
			}
		}
	}
	return res, nil
}

func targetKeys(ms []genre.Match) []string {
	var out []string
	for _, m := range ms {
		if m.Kind == genre.Prose {
			continue
		}
		k := m.Target.Name
		if m.Target.Custom {
			k = "~" + genre.Key(k)
		}
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
