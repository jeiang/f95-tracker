package games

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// PlayLogAdded is the result of marking a version played. FirstUserEntry is
// true when this was the Game's first user entry (R-UI-4 redirect decision).
type PlayLogAdded struct {
	Entry          PlayLog
	FirstUserEntry bool
}

func checkDate(d string) error {
	if _, err := clock.ParseDate(d); err != nil {
		return invalid("date %q must be YYYY-MM-DD", d)
	}
	return nil
}

// AddPlayLog records a user entry. playedOn defaults to today; back-dating is allowed.
func (s *Service) AddPlayLog(ctx context.Context, gameID int64, version, playedOn string) (PlayLogAdded, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return PlayLogAdded{}, invalid("version is required")
	}
	if playedOn == "" {
		playedOn = clock.Date(s.clock.Now())
	} else if err := checkDate(playedOn); err != nil {
		return PlayLogAdded{}, err
	}
	var out PlayLogAdded
	err := s.exec(ctx, gameID, func(q *sqlcgen.Queries, now string) error {
		n, err := q.CountUserPlayLog(ctx, gameID)
		if err != nil {
			return err
		}
		out.FirstUserEntry = n == 0
		out.Entry, err = q.InsertPlayLog(ctx, sqlcgen.InsertPlayLogParams{
			GameID: gameID, Version: version, PlayedOn: nullStr(playedOn),
			Origin: string(domain.PlayLogUser), CreatedAt: now,
		})
		return err
	})
	return out, err
}

// EditPlayLog corrects an entry's version and date. User entries must stay
// dated; an imported entry may keep an empty date.
func (s *Service) EditPlayLog(ctx context.Context, entryID int64, version, playedOn string) (PlayLog, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return PlayLog{}, invalid("version is required")
	}
	if playedOn != "" {
		if err := checkDate(playedOn); err != nil {
			return PlayLog{}, err
		}
	}
	var out PlayLog
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		e, err := q.GetPlayLog(ctx, entryID)
		if err != nil {
			return mapNoRows(err, "play log entry", entryID)
		}
		if playedOn == "" && e.Origin == string(domain.PlayLogUser) {
			return invalid("date is required for a user entry")
		}
		out, err = q.UpdatePlayLog(ctx, sqlcgen.UpdatePlayLogParams{Version: version, PlayedOn: nullStr(playedOn), ID: entryID})
		return err
	})
	return out, err
}

// DeletePlayLog removes an entry; last played is recomputed by the game_last_played view.
func (s *Service) DeletePlayLog(ctx context.Context, entryID int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetPlayLog(ctx, entryID); err != nil {
			return mapNoRows(err, "play log entry", entryID)
		}
		if err := q.ClearTagReviewPlayLogRef(ctx, sql.NullInt64{Int64: entryID, Valid: true}); err != nil {
			return err
		}
		return q.DeletePlayLog(ctx, entryID)
	})
}
