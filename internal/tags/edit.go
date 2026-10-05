package tags

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/genre"
)

func (s *Service) gameTagTx(ctx context.Context, q *sqlcgen.Queries, id int64) (sqlcgen.GameTag, error) {
	row, err := q.GetGameTag(ctx, id)
	return row, mapNoRows(err, "game tag", id)
}

// AddByHand adds a tag to any Game as confirmed with origin manual (R-TAG-6).
// A tag the Game already has is confirmed instead, keeping its stronger
// qualifier; one that was removed at source becomes the user's own (manual).
func (s *Service) AddByHand(ctx context.Context, gameID int64, ref TagRef, qualifier string) (sqlcgen.GameTag, error) {
	if qualifier == "" {
		qualifier = QualPresent
	}
	if !validQualifier(qualifier) {
		return sqlcgen.GameTag{}, invalid("qualifier %q", qualifier)
	}
	var out sqlcgen.GameTag
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetGame(ctx, gameID); err != nil {
			return mapNoRows(err, "game", gameID)
		}
		tag, err := s.EnsureTagTx(ctx, q, ref)
		if err != nil {
			return err
		}
		verified := nullStr(s.now())
		row, err := q.GetGameTagByTag(ctx, sqlcgen.GetGameTagByTagParams{GameID: gameID, TagID: tag.ID})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out, err = q.InsertGameTag(ctx, sqlcgen.InsertGameTagParams{
				GameID: gameID, TagID: tag.ID, Origin: OriginManual, Qualifier: qualifier, Verification: Confirmed, VerifiedAt: verified,
			})
			return err
		case err != nil:
			return err
		}
		if rank(qualifier) > rank(row.Qualifier) {
			row.Qualifier = qualifier
		}
		row.Verification, row.VerifiedAt = Confirmed, verified
		if row.RemovedAtSourceAt.Valid {
			row.Origin, row.RemovedAtSourceAt, row.F95Only = OriginManual, sql.NullString{}, 0
		}
		out = row
		return q.UpdateGameTag(ctx, updateParams(row))
	})
	return out, err
}

// SetVerification sets a Game tag's verification by hand, on any qualifier (R-TAG-6).
func (s *Service) SetVerification(ctx context.Context, gameTagID int64, verification string) error {
	if verification != Unverified && verification != Confirmed && verification != Wrong {
		return invalid("verification %q", verification)
	}
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := s.gameTagTx(ctx, q, gameTagID)
		if err != nil {
			return err
		}
		row.Verification = verification
		row.VerifiedAt = nullStr("")
		if verification != Unverified {
			row.VerifiedAt = nullStr(s.now())
		}
		return q.UpdateGameTag(ctx, updateParams(row))
	})
}

// SetQualifier edits a Game tag's qualifier by hand.
func (s *Service) SetQualifier(ctx context.Context, gameTagID int64, qualifier string) error {
	if !validQualifier(qualifier) {
		return invalid("qualifier %q", qualifier)
	}
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := s.gameTagTx(ctx, q, gameTagID)
		if err != nil {
			return err
		}
		row.Qualifier = qualifier
		return q.UpdateGameTag(ctx, updateParams(row))
	})
}

// SetMapping re-points a Game tag that came from a Genre phrase to another tag.
// The row is marked mapping_override, and the phrase is saved as a user Synonym
// (re-applied to other Games' unverified tags) unless it is a compound "a/b"
// phrase or already an F95 slug. Colliding with a tag the Game already has
// collapses the two rows (R-TAG-4).
func (s *Service) SetMapping(ctx context.Context, gameTagID int64, target TagRef) (ReapplyResult, error) {
	var res ReapplyResult
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := s.gameTagTx(ctx, q, gameTagID)
		if err != nil {
			return err
		}
		if !row.SourcePhrase.Valid {
			return invalid("game tag %d has no Genre phrase to remap", gameTagID)
		}
		tag, err := s.EnsureTagTx(ctx, q, target)
		if err != nil {
			return err
		}
		if _, err := repointTx(ctx, q, row, tag.ID, true); err != nil {
			return err
		}
		key := ""
		if items := genre.Parse(row.SourcePhrase.String); len(items) == 1 && !strings.Contains(items[0].Phrase, "/") {
			key = genre.Key(items[0].Phrase)
		}
		if key == "" {
			return nil
		}
		vocab, _, err := loadVocab(ctx, q)
		if err != nil {
			return err
		}
		if _, isSlug := vocab[key]; isSlug {
			return nil
		}
		res, err = s.saveUserSynonymTx(ctx, q, key, tag.ID)
		return err
	})
	return res, err
}

// DismissRemoved drops a tag that vanished from the Source (removed_at_source_at
// set); refresh would only flag it again if it stayed (R-TAG-7).
func (s *Service) DismissRemoved(ctx context.Context, gameTagID int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := s.gameTagTx(ctx, q, gameTagID)
		if err != nil {
			return err
		}
		if !row.RemovedAtSourceAt.Valid {
			return invalid("game tag %d is not removed at source", gameTagID)
		}
		return q.DeleteGameTag(ctx, gameTagID)
	})
}
