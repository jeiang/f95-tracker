package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func TestMigrationSeeds(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	q := s.Queries()

	// 154 vocabulary rows minus `unknown`; 29 SYN entries minus the two None ones.
	if n, err := q.CountTagsByKind(ctx, "f95"); err != nil || n != 153 {
		t.Fatalf("f95 tags = %d, %v; want 153", n, err)
	}
	if n, _ := q.CountTagsByKind(ctx, "custom"); n != 0 {
		t.Fatalf("custom tags = %d, want 0", n)
	}
	if st, err := q.GetSettings(ctx); err != nil || st.PlatformPref != "linux" || st.MirrorHostOrder != `["pixeldrain","mega","gofile"]` {
		t.Fatalf("settings = %+v, %v", st, err)
	}
	if n, err := q.CountSynonymsByOrigin(ctx, "seed"); err != nil || n != 27 {
		t.Fatalf("seed synonyms = %d, %v; want 27", n, err)
	}
	if got, err := q.ListAlertPlayStatuses(ctx); err != nil || !reflect.DeepEqual(got, []string{"on_hold", "planned", "playing"}) {
		t.Fatalf("alert set = %v, %v", got, err)
	}
}

func TestOpenIsIdempotentAndSecure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "f95-tracker.db")
	ctx := context.Background()
	s, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = db.Open(ctx, path) // second open: migrations already applied
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s mode = %v, %v; want %v", p, fi.Mode().Perm(), err, want)
			}
		}
	}
}

func TestWithTxRollbackAndReadPoolIsReadOnly(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	boom := os.ErrDeadlineExceeded
	err := s.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := q.InsertGame(ctx, sqlcgen.InsertGameParams{Name: "x", PlayStatus: "planned", AddedAt: "t", UpdatedAt: "t"}); err != nil {
			return err
		}
		return boom
	})
	if err != boom {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Queries().GetGame(ctx, 1); err != sql.ErrNoRows {
		t.Fatalf("rolled back game visible: %v", err)
	}
	if _, err := s.Queries().InsertGame(ctx, sqlcgen.InsertGameParams{Name: "x", PlayStatus: "planned", AddedAt: "t", UpdatedAt: "t"}); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

type vectors struct {
	Normalize []struct{ Name, In, Want string } `json:"normalize"`
	Behind    []struct {
		Name, Latest, Played string
		Behind               bool
	} `json:"behind"`
}

func loadVectors(t *testing.T) vectors {
	raw, err := os.ReadFile(filepath.Join("..", "domain", "testdata", "version_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// R-TEST-3: the SQL generated columns agree with the shared vectors (and so with domain.NormalizeVersion).
func TestVersionNormSQLMatchesVectors(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	for i, c := range loadVectors(t).Normalize {
		err := s.WithTx(ctx, func(q *sqlcgen.Queries) error {
			g, err := q.InsertGame(ctx, sqlcgen.InsertGameParams{Name: c.Name, PlayStatus: "planned", AddedAt: "t", UpdatedAt: "t"})
			if err != nil {
				return err
			}
			if _, err = q.InsertSource(ctx, sqlcgen.InsertSourceParams{
				GameID: g.ID, Kind: "manual", IsPrimary: 1, Url: "u", CreatedAt: "t",
				LatestVersion: sql.NullString{String: c.In, Valid: true},
			}); err != nil {
				return err
			}
			pl, err := q.InsertPlayLog(ctx, sqlcgen.InsertPlayLogParams{
				GameID: g.ID, Version: c.In, PlayedOn: sql.NullString{String: "2026-01-01", Valid: true}, Origin: "user", CreatedAt: "t",
			})
			if err != nil {
				return err
			}
			src, err := q.GetPrimarySource(ctx, g.ID)
			if err != nil {
				return err
			}
			if src.LatestVersionNorm.String != c.Want || !src.LatestVersionNorm.Valid {
				t.Errorf("#%d %s: latest_version_norm(%q) = %q, want %q", i, c.Name, c.In, src.LatestVersionNorm.String, c.Want)
			}
			if pl.VersionNorm.String != c.Want {
				t.Errorf("#%d %s: version_norm(%q) = %q, want %q", i, c.Name, c.In, pl.VersionNorm.String, c.Want)
			}
			if got := domain.NormalizeVersion(c.In); got != src.LatestVersionNorm.String {
				t.Errorf("#%d %s: Go %q != SQL %q", i, c.Name, got, src.LatestVersionNorm.String)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestGameBehindViewMatchesVectors(t *testing.T) {
	s := testutil.NewStore(t)
	cases := loadVectors(t).Behind
	ids := make(map[int64]int)
	for i, c := range cases {
		g, _ := testutil.InsertGame(t, s, testutil.GameSpec{Name: c.Name, LatestVersion: c.Latest})
		testutil.InsertPlayLog(t, s, g.ID, c.Played, "2026-02-01")
		ids[g.ID] = i
	}
	behind, err := s.Queries().ListBehindGameIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int]bool)
	for _, id := range behind {
		got[ids[id]] = true
	}
	for i, c := range cases {
		if got[i] != c.Behind {
			t.Errorf("%s: latest %q vs played %q: behind=%v, want %v", c.Name, c.Latest, c.Played, got[i], c.Behind)
		}
	}
}

func TestGameBehindEdgeCases(t *testing.T) {
	s := testutil.NewStore(t)
	// Empty play log is never behind.
	testutil.InsertGame(t, s, testutil.GameSpec{Name: "empty log", LatestVersion: "2.0"})
	// Date-only Source: behind only when updated after the newest dated entry.
	late, _ := testutil.InsertGame(t, s, testutil.GameSpec{Name: "date-only late", Kind: domain.SourceItchio, ThreadUpdatedAt: "2026-03-01T10:00:00Z"})
	testutil.InsertPlayLog(t, s, late.ID, "x", "2026-02-01")
	same, _ := testutil.InsertGame(t, s, testutil.GameSpec{Name: "date-only same day", Kind: domain.SourceItchio, ThreadUpdatedAt: "2026-02-01T23:00:00Z"})
	testutil.InsertPlayLog(t, s, same.ID, "x", "2026-02-01")
	// Imported (undated) entry cannot make a date-only Source behind.
	imp, _ := testutil.InsertGame(t, s, testutil.GameSpec{Name: "date-only imported", Kind: domain.SourceItchio, ThreadUpdatedAt: "2026-03-01T10:00:00Z"})
	testutil.InsertPlayLog(t, s, imp.ID, "x", "")

	ids, err := s.Queries().ListBehindGameIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != late.ID {
		t.Fatalf("behind = %v, want only %d", ids, late.ID)
	}
}
