package games

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// GameDetail is the Game detail aggregate. Behind and HasUpdate are false
// unless the Game's Play status is in the alert set (R-UPD-9).
type GameDetail struct {
	Game       Game
	Primary    Source
	Sources    []Source // primary first
	PlayLog    []PlayLog
	LastPlayed *sqlcgen.GameLastPlayed
	Alerting   bool
	Behind     bool
	HasUpdate  bool
}

func (s *Service) Detail(ctx context.Context, gameID int64) (GameDetail, error) {
	q := s.store.Queries()
	var d GameDetail
	var err error
	if d.Game, err = q.GetGame(ctx, gameID); err != nil {
		return d, mapNoRows(err, "game", gameID)
	}
	if d.Sources, err = q.ListSourcesByGame(ctx, gameID); err != nil {
		return d, err
	}
	if i := slices.IndexFunc(d.Sources, func(s Source) bool { return s.IsPrimary == 1 }); i >= 0 {
		d.Primary = d.Sources[i]
	}
	if d.PlayLog, err = q.ListPlayLog(ctx, gameID); err != nil {
		return d, err
	}
	lp, err := q.GetGameLastPlayed(ctx, gameID)
	switch {
	case err == nil:
		d.LastPlayed = &lp
	case !errors.Is(err, sql.ErrNoRows):
		return d, err
	}
	f, err := q.GetGameFlags(ctx, gameID)
	if err != nil {
		return d, err
	}
	d.Alerting, d.Behind, d.HasUpdate = f.Alerting == 1, f.Behind == 1, f.HasUpdate == 1
	return d, nil
}

type Sort string

const (
	SortDefault    Sort = "default" // Update/Behind first, then most recently updated at the Source
	SortName       Sort = "name"
	SortRating     Sort = "rating"
	SortPlayStatus Sort = "play_status" // playing, on hold, planned, finished, dropped
	SortLastPlayed Sort = "last_played"
	SortAdded      Sort = "added"
)

// ListFilter selects and orders the Game list. Zero value = everything, default sort.
type ListFilter struct {
	Name          string // case-insensitive substring
	PlayStatus    domain.PlayStatus
	DevStatus     domain.DevStatus
	IncludeTags   []int64 // all must match (AND)
	ExcludeTags   []int64 // none may match
	AllQualifiers bool    // also match planned/optional tags (default: present only)
	MinRatingX2   int     // 0 = any
	Behind        bool
	Updates       bool
	Sort          Sort
	Desc          *bool // nil = the sort's natural direction
}

func (f ListFilter) params() (sqlcgen.ListGamesParams, error) {
	if f.PlayStatus != "" && !f.PlayStatus.Valid() {
		return sqlcgen.ListGamesParams{}, invalid("play status %q", f.PlayStatus)
	}
	if f.DevStatus != "" && !f.DevStatus.Valid() {
		return sqlcgen.ListGamesParams{}, invalid("dev status %q", f.DevStatus)
	}
	desc := false
	switch f.Sort {
	case "":
		f.Sort = SortDefault
	case SortDefault:
	case SortName, SortPlayStatus:
	case SortRating, SortLastPlayed, SortAdded:
		desc = true
	default:
		return sqlcgen.ListGamesParams{}, invalid("sort %q", f.Sort)
	}
	if f.Desc != nil {
		desc = *f.Desc
	}
	dir := "asc"
	if desc {
		dir = "desc"
	}
	ids := func(v []int64) string {
		if v == nil {
			v = []int64{}
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	return sqlcgen.ListGamesParams{
		NameText:      f.Name,
		PlayStatus:    nullStr(string(f.PlayStatus)),
		DevStatus:     nullStr(string(f.DevStatus)),
		MinRatingX2:   sql.NullInt64{Int64: int64(f.MinRatingX2), Valid: f.MinRatingX2 > 0},
		BehindOnly:    boolInt(f.Behind),
		UpdatesOnly:   boolInt(f.Updates),
		IncludeTags:   ids(f.IncludeTags),
		ExcludeTags:   ids(f.ExcludeTags),
		AllQualifiers: boolInt(f.AllQualifiers),
		Sort:          string(f.Sort),
		Dir:           dir,
	}, nil
}

// List returns Games (with primary Source summary and badge flags) per the filter and sort.
func (s *Service) List(ctx context.Context, f ListFilter) ([]sqlcgen.ListGamesRow, error) {
	p, err := f.params()
	if err != nil {
		return nil, err
	}
	return s.store.Queries().ListGames(ctx, p)
}

// PlayStatusCounts is the number of Games per Play status (zero-filled), for the alert-set settings.
func (s *Service) PlayStatusCounts(ctx context.Context) (map[domain.PlayStatus]int64, error) {
	rows, err := s.store.Queries().CountGamesByPlayStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := map[domain.PlayStatus]int64{
		domain.PlayPlanned: 0, domain.PlayPlaying: 0, domain.PlayFinished: 0, domain.PlayDropped: 0, domain.PlayOnHold: 0,
	}
	for _, r := range rows {
		out[domain.PlayStatus(r.PlayStatus)] = r.N
	}
	return out, nil
}

func (s *Service) AlertSet(ctx context.Context) ([]domain.PlayStatus, error) {
	rows, err := s.store.Queries().ListAlertPlayStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PlayStatus, len(rows))
	for i, r := range rows {
		out[i] = domain.PlayStatus(r)
	}
	return out, nil
}

// SetAlertSet replaces the alert set. An empty set is allowed (no Game alerts).
func (s *Service) SetAlertSet(ctx context.Context, set []domain.PlayStatus) error {
	for _, ps := range set {
		if !ps.Valid() {
			return invalid("play status %q", ps)
		}
	}
	set = slices.Compact(slices.Sorted(slices.Values(set)))
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteAlertPlayStatuses(ctx); err != nil {
			return err
		}
		for _, ps := range set {
			if err := q.InsertAlertPlayStatus(ctx, string(ps)); err != nil {
				return err
			}
		}
		return nil
	})
}

type Settings struct {
	PlatformPref    domain.PlatformPref
	MirrorHostOrder []string
}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	r, err := s.store.Queries().GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	out := Settings{PlatformPref: domain.PlatformPref(r.PlatformPref)}
	if err := json.Unmarshal([]byte(r.MirrorHostOrder), &out.MirrorHostOrder); err != nil {
		return Settings{}, err
	}
	return out, nil
}

func (s *Service) SetSettings(ctx context.Context, v Settings) error {
	if !v.PlatformPref.Valid() {
		return invalid("platform preference %q", v.PlatformPref)
	}
	seen := map[string]bool{}
	for _, h := range v.MirrorHostOrder {
		if h == "" || seen[h] {
			return invalid("mirror host order has an empty or repeated host")
		}
		seen[h] = true
	}
	order := v.MirrorHostOrder
	if order == nil {
		order = []string{}
	}
	b, _ := json.Marshal(order)
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		return q.UpdateSettings(ctx, sqlcgen.UpdateSettingsParams{
			MirrorHostOrder: string(b), PlatformPref: string(v.PlatformPref), UpdatedAt: s.now(),
		})
	})
}
