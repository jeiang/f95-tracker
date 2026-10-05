package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// NewStore opens a migrated database in a temp dir, closed when the test ends.
func NewStore(t testing.TB) *db.Store {
	t.Helper()
	s, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "state", "f95-tracker.db"))
	if err != nil {
		t.Fatalf("testutil.NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// GameSpec describes a Game and its primary Source; zero values get defaults.
// Empty LatestVersion, ChangeKey, DevStatus and ThreadUpdatedAt are stored as NULL.
type GameSpec struct {
	Name            string
	PlayStatus      domain.PlayStatus // default planned
	RatingX2        int               // 0 = no rating
	ImportReview    bool
	Kind            domain.SourceKind // default f95_thread
	ExternalID      string            // default unique number; ignored for manual
	URL             string
	LatestVersion   string
	ChangeKey       string // default LatestVersion for non-manual Sources
	DevStatus       domain.DevStatus
	ThreadUpdatedAt string    // UTC timestamp text
	At              time.Time // added/created time; default 2026-01-01T00:00:00Z
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// InsertGame stores the Game and its primary Source in one transaction.
func InsertGame(t testing.TB, s *db.Store, spec GameSpec) (sqlcgen.Game, sqlcgen.Source) {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "Test Game"
	}
	if spec.PlayStatus == "" {
		spec.PlayStatus = domain.PlayPlanned
	}
	if spec.Kind == "" {
		spec.Kind = domain.SourceF95Thread
	}
	if spec.At.IsZero() {
		spec.At = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	at := clock.Timestamp(spec.At)
	var g sqlcgen.Game
	var src sqlcgen.Source
	err := s.WithTx(context.Background(), func(q *sqlcgen.Queries) error {
		var err error
		rating := sql.NullInt64{Int64: int64(spec.RatingX2), Valid: spec.RatingX2 != 0}
		review := int64(0)
		if spec.ImportReview {
			review = 1
		}
		g, err = q.InsertGame(context.Background(), sqlcgen.InsertGameParams{
			Name: spec.Name, PlayStatus: string(spec.PlayStatus), RatingX2: rating,
			ImportReview: review, AddedAt: at, UpdatedAt: at,
		})
		if err != nil {
			return err
		}
		ext := spec.ExternalID
		if spec.Kind == domain.SourceManual {
			ext = ""
		} else if ext == "" {
			ext = fmt.Sprint(1000 + g.ID)
		}
		url := spec.URL
		if url == "" {
			url = fmt.Sprintf("https://example.invalid/%d", g.ID)
		}
		key := spec.ChangeKey
		if key == "" && spec.Kind != domain.SourceManual {
			key = spec.LatestVersion
		}
		src, err = q.InsertSource(context.Background(), sqlcgen.InsertSourceParams{
			GameID: g.ID, Kind: string(spec.Kind), IsPrimary: 1, ExternalID: nullStr(ext), Url: url,
			LatestVersion: nullStr(spec.LatestVersion), ChangeKey: nullStr(key),
			DevStatus: nullStr(string(spec.DevStatus)), ThreadUpdatedAt: nullStr(spec.ThreadUpdatedAt), CreatedAt: at,
		})
		return err
	})
	if err != nil {
		t.Fatalf("testutil.InsertGame: %v", err)
	}
	return g, src
}

// InsertPlayLog adds a user entry (playedOn = YYYY-MM-DD) or, with an empty
// playedOn, an imported undated entry.
func InsertPlayLog(t testing.TB, s *db.Store, gameID int64, version, playedOn string) sqlcgen.PlayLog {
	t.Helper()
	origin := domain.PlayLogUser
	if playedOn == "" {
		origin = domain.PlayLogImported
	}
	var pl sqlcgen.PlayLog
	err := s.WithTx(context.Background(), func(q *sqlcgen.Queries) error {
		var err error
		pl, err = q.InsertPlayLog(context.Background(), sqlcgen.InsertPlayLogParams{
			GameID: gameID, Version: version, PlayedOn: nullStr(playedOn),
			Origin: string(origin), CreatedAt: "2026-01-01T00:00:00Z",
		})
		return err
	})
	if err != nil {
		t.Fatalf("testutil.InsertPlayLog: %v", err)
	}
	return pl
}
