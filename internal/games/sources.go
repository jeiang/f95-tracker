package games

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// AddLinkSource lists another Source for the Game as a link only (not checked).
func (s *Service) AddLinkSource(ctx context.Context, gameID int64, sp SourceSpec) (Source, error) {
	if err := sp.validate(); err != nil {
		return Source{}, err
	}
	var out Source
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.GetGame(ctx, gameID); err != nil {
			return mapNoRows(err, "game", gameID)
		}
		if err := checkUntracked(ctx, q, sp); err != nil {
			return err
		}
		var err error
		out, err = q.InsertSource(ctx, sqlcgen.InsertSourceParams{
			GameID: gameID, Kind: string(sp.Kind), ExternalID: nullStr(sp.ExternalID), Url: sp.URL, CreatedAt: s.now(),
		})
		return err
	})
	return out, err
}

// SetPrimary makes sourceID the Game's primary Source. The previous primary
// becomes a link: its check fields are cleared and its queued detail fetch dropped.
func (s *Service) SetPrimary(ctx context.Context, sourceID int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		src, err := q.GetSource(ctx, sourceID)
		if err != nil {
			return mapNoRows(err, "source", sourceID)
		}
		if src.IsPrimary == 1 {
			return nil
		}
		old, err := q.GetPrimarySource(ctx, src.GameID)
		if err != nil {
			return err
		}
		if err := q.DemoteSource(ctx, old.ID); err != nil {
			return err
		}
		if err := q.DeleteDetailFetchQueue(ctx, old.ID); err != nil {
			return err
		}
		return q.PromoteSource(ctx, sourceID)
	})
}

func (s *Service) primaryOf(ctx context.Context, q *sqlcgen.Queries, gameID int64) (Source, error) {
	src, err := q.GetPrimarySource(ctx, gameID)
	return src, mapNoRows(err, "game", gameID)
}

// SetDevStatus edits the Dev status of the Game's primary Source; nil clears it.
// F95 Sources are read-only (R-UI-9, INV-2).
func (s *Service) SetDevStatus(ctx context.Context, gameID int64, ds *domain.DevStatus) error {
	if ds != nil && !ds.Valid() {
		return invalid("dev status %q", *ds)
	}
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		src, err := s.primaryOf(ctx, q, gameID)
		if err != nil {
			return err
		}
		if src.Kind == string(domain.SourceF95Thread) {
			return invalid("dev status of an F95 thread comes from F95 and cannot be edited")
		}
		var v string
		if ds != nil {
			v = string(*ds)
		}
		return q.SetSourceDevStatus(ctx, sqlcgen.SetSourceDevStatusParams{DevStatus: nullStr(v), ID: src.ID})
	})
}

// ReenableChecks re-enables checks on the Game's primary Source after it was
// marked unavailable or disabled.
func (s *Service) ReenableChecks(ctx context.Context, gameID int64) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		src, err := s.primaryOf(ctx, q, gameID)
		if err != nil {
			return err
		}
		return q.ReenableSource(ctx, src.ID)
	})
}

// Detail is the data a detail fetch learned about a primary Source. nil/empty
// fields leave the stored value alone.
type Detail struct {
	Name            *string
	LatestVersion   *string
	ChangeKey       *string
	DevStatus       *domain.DevStatus
	ThreadUpdatedAt *string
	GenreText       *string // raw Genre text of a logged-in F95 fetch; ignored for other kinds
	CoverURL        string
	DetailAt        time.Time
	DetailsPending  bool
}

// DetailResult tells the caller whether to fetch the cover (after its tx commits).
type DetailResult struct {
	GameID     int64
	FetchCover bool
	CoverURL   string
}

// ApplySourceDetail writes a detail fetch to the primary Source inside the
// caller's transaction. F95 Dev status always overwrites and the F95 title
// renames the Game; for other kinds both are ignored (user-owned, INV-2).
// It does not record check results or decide Updates.
func (s *Service) ApplySourceDetail(ctx context.Context, q *sqlcgen.Queries, sourceID int64, d Detail) (DetailResult, error) {
	src, err := q.GetSource(ctx, sourceID)
	if err != nil {
		return DetailResult{}, mapNoRows(err, "source", sourceID)
	}
	if src.IsPrimary != 1 {
		return DetailResult{}, invalid("source %d is not primary", sourceID)
	}
	f95 := src.Kind == string(domain.SourceF95Thread)
	p := sqlcgen.ApplySourceDetailParams{
		LatestVersion: nullPtr(d.LatestVersion), ChangeKey: nullPtr(d.ChangeKey),
		ThreadUpdatedAt: nullPtr(d.ThreadUpdatedAt), DetailAt: nullStr(clock.Timestamp(d.DetailAt)),
		DetailsPending: boolInt(d.DetailsPending), ID: sourceID,
	}
	if d.DetailAt.IsZero() {
		p.DetailAt = nullStr(s.now())
	}
	if f95 && d.DevStatus != nil {
		if !d.DevStatus.Valid() {
			return DetailResult{}, invalid("dev status %q", *d.DevStatus)
		}
		p.DevStatus = nullStr(string(*d.DevStatus))
	}
	if f95 {
		p.GenreText = nullPtr(d.GenreText)
	}
	if src.Kind == string(domain.SourceManual) && d.ChangeKey != nil {
		return DetailResult{}, invalid("manual source has no change key")
	}
	if err := q.ApplySourceDetail(ctx, p); err != nil {
		return DetailResult{}, err
	}
	g, err := q.GetGame(ctx, src.GameID)
	if err != nil {
		return DetailResult{}, err
	}
	if f95 && d.Name != nil {
		if n := strings.TrimSpace(*d.Name); n != "" && n != g.Name {
			if err := q.UpdateGameName(ctx, sqlcgen.UpdateGameNameParams{Name: n, UpdatedAt: s.now(), ID: g.ID}); err != nil {
				return DetailResult{}, err
			}
		}
	}
	res := DetailResult{GameID: g.ID}
	if d.CoverURL != "" {
		res.CoverURL = d.CoverURL
		res.FetchCover = g.CoverSourceUrl.String != d.CoverURL || !g.CoverPath.Valid || !s.coverFileExists(g.CoverPath.String)
	}
	return res, nil
}

func (s *Service) coverFileExists(rel string) bool {
	_, err := os.Stat(filepath.Join(s.state, rel))
	return err == nil
}

// SetBaseline records the version a Source is first seen at (import, add) with
// no check_result, so no Update is raised for it (R-UPD-4).
func (s *Service) SetBaseline(ctx context.Context, q *sqlcgen.Queries, sourceID int64, version, changeKey string) error {
	src, err := q.GetSource(ctx, sourceID)
	if err != nil {
		return mapNoRows(err, "source", sourceID)
	}
	if src.IsPrimary != 1 {
		return invalid("source %d is not primary", sourceID)
	}
	if src.Kind == string(domain.SourceManual) && changeKey != "" {
		return invalid("manual source has no change key")
	}
	return q.SetSourceBaseline(ctx, sqlcgen.SetSourceBaselineParams{
		LatestVersion: nullStr(version), ChangeKey: nullStr(changeKey), ID: sourceID,
	})
}
