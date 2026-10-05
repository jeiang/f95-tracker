package games

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// CreateParams describes a new Game and its primary Source. The version fields
// carry the baseline (import) or the guest data (add); empty means NULL.
type CreateParams struct {
	Name         string
	PlayStatus   domain.PlayStatus // default planned
	RatingX2     int               // 0 = no rating
	ImportReview bool
	Source       SourceSpec

	LatestVersion   string
	ChangeKey       string // must be empty for manual Sources
	DevStatus       domain.DevStatus
	ThreadUpdatedAt string
	DetailsPending  bool
}

type Created struct {
	Game   Game
	Source Source
}

// Create adds a Game with its primary Source in its own transaction.
func (s *Service) Create(ctx context.Context, p CreateParams) (Created, error) {
	var out Created
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = s.CreateTx(ctx, q, p)
		return err
	})
	return out, err
}

// CreateTx is Create inside the caller's transaction. A Source that is already
// tracked yields a *ConflictError (matches domain.ErrConflict) carrying the Game id.
func (s *Service) CreateTx(ctx context.Context, q *sqlcgen.Queries, p CreateParams) (Created, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return Created{}, invalid("name is required")
	}
	if p.PlayStatus == "" {
		p.PlayStatus = domain.PlayPlanned
	}
	if !p.PlayStatus.Valid() {
		return Created{}, invalid("play status %q", p.PlayStatus)
	}
	if p.RatingX2 != 0 && (p.RatingX2 < 1 || p.RatingX2 > 10) {
		return Created{}, invalid("rating out of range")
	}
	if p.DevStatus != "" && !p.DevStatus.Valid() {
		return Created{}, invalid("dev status %q", p.DevStatus)
	}
	if err := p.Source.validate(); err != nil {
		return Created{}, err
	}
	if p.Source.Kind == domain.SourceManual && p.ChangeKey != "" {
		return Created{}, invalid("manual source has no change key")
	}
	if err := checkUntracked(ctx, q, p.Source); err != nil {
		return Created{}, err
	}
	now := s.now()
	g, err := q.InsertGame(ctx, sqlcgen.InsertGameParams{
		Name: name, PlayStatus: string(p.PlayStatus),
		RatingX2:     sql.NullInt64{Int64: int64(p.RatingX2), Valid: p.RatingX2 != 0},
		ImportReview: boolInt(p.ImportReview), AddedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Created{}, err
	}
	src, err := q.InsertSource(ctx, sqlcgen.InsertSourceParams{
		GameID: g.ID, Kind: string(p.Source.Kind), IsPrimary: 1, ExternalID: nullStr(p.Source.ExternalID),
		Url: p.Source.URL, LatestVersion: nullStr(p.LatestVersion), ChangeKey: nullStr(p.ChangeKey),
		DevStatus: nullStr(string(p.DevStatus)), ThreadUpdatedAt: nullStr(p.ThreadUpdatedAt), CreatedAt: now,
	})
	if err != nil {
		return Created{}, err
	}
	if p.DetailsPending {
		if err := q.SetSourceDetailsPending(ctx, sqlcgen.SetSourceDetailsPendingParams{DetailsPending: 1, ID: src.ID}); err != nil {
			return Created{}, err
		}
		src.DetailsPending = 1
	}
	return Created{Game: g, Source: src}, nil
}

// Remove deletes the Game with its Sources, Play log, tags and jobs, and its cached cover.
func (s *Service) Remove(ctx context.Context, gameID int64) error {
	var cover string
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		g, err := q.GetGame(ctx, gameID)
		if err != nil {
			return mapNoRows(err, "game", gameID)
		}
		cover = g.CoverPath.String
		return q.DeleteGame(ctx, gameID)
	})
	if err == nil && cover != "" {
		os.Remove(filepath.Join(s.state, cover))
	}
	return err
}
