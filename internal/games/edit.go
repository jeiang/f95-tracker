package games

import (
	"context"
	"database/sql"
	"math"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// RatingX2 converts stars (0.5..5 in 0.5 steps) to the stored value.
func RatingX2(stars float64) (int, error) {
	x2 := stars * 2
	if x2 < 1 || x2 > 10 || x2 != math.Trunc(x2) {
		return 0, invalid("rating %v must be 0.5 to 5 in steps of 0.5", stars)
	}
	return int(x2), nil
}

func (s *Service) exec(ctx context.Context, gameID int64, fn func(q *sqlcgen.Queries, now string) error) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetGame(ctx, gameID); err != nil {
			return mapNoRows(err, "game", gameID)
		}
		return fn(q, s.now())
	})
}

// SetPlayStatus is the only writer of Play status besides the CSV import.
func (s *Service) SetPlayStatus(ctx context.Context, gameID int64, ps domain.PlayStatus) error {
	if !ps.Valid() {
		return invalid("play status %q", ps)
	}
	return s.exec(ctx, gameID, func(q *sqlcgen.Queries, now string) error {
		return q.UpdateGamePlayStatus(ctx, sqlcgen.UpdateGamePlayStatusParams{PlayStatus: string(ps), UpdatedAt: now, ID: gameID})
	})
}

// SetRating stores stars x2; nil clears the rating.
func (s *Service) SetRating(ctx context.Context, gameID int64, stars *float64) error {
	var v sql.NullInt64
	if stars != nil {
		x2, err := RatingX2(*stars)
		if err != nil {
			return err
		}
		v = sql.NullInt64{Int64: int64(x2), Valid: true}
	}
	return s.exec(ctx, gameID, func(q *sqlcgen.Queries, now string) error {
		return q.UpdateGameRating(ctx, sqlcgen.UpdateGameRatingParams{RatingX2: v, UpdatedAt: now, ID: gameID})
	})
}

// SetPlatformPref sets the per-Game platform override; nil falls back to the default.
func (s *Service) SetPlatformPref(ctx context.Context, gameID int64, pp *domain.PlatformPref) error {
	var v string
	if pp != nil {
		if !pp.Valid() {
			return invalid("platform preference %q", *pp)
		}
		v = string(*pp)
	}
	return s.exec(ctx, gameID, func(q *sqlcgen.Queries, now string) error {
		return q.UpdateGamePlatformPref(ctx, sqlcgen.UpdateGamePlatformPrefParams{PlatformPref: nullStr(v), UpdatedAt: now, ID: gameID})
	})
}

// ClearImportReview takes Games off the CSV-import review list.
func (s *Service) ClearImportReview(ctx context.Context, gameIDs ...int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		now := s.now()
		for _, id := range gameIDs {
			if _, err := q.GetGame(ctx, id); err != nil {
				return mapNoRows(err, "game", id)
			}
			if err := q.ClearGameImportReview(ctx, sqlcgen.ClearGameImportReviewParams{UpdatedAt: now, ID: id}); err != nil {
				return err
			}
		}
		return nil
	})
}
