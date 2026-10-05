package games_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/testutil"
	_ "modernc.org/sqlite"
)

var ctx = context.Background()

type env struct {
	t     *testing.T
	svc   *games.Service
	store *db.Store
	raw   *sql.DB // second connection for fixtures that have no service API (tags, check results)
	clk   *clock.Fake
	state string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "f95-tracker.db")
	store, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	clk := clock.NewFake(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	state := filepath.Join(dir, "state")
	return &env{t: t, store: store, raw: raw, clk: clk, state: state,
		svc: games.New(store, clk, games.Options{StateDir: state})}
}

func (e *env) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.raw.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) tag(gameID int64, slug, qualifier, verification string, removed bool) int64 {
	e.t.Helper()
	var id int64
	if err := e.raw.QueryRow(`SELECT id FROM tag WHERE slug = ?`, slug).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	var rem any
	if removed {
		rem = "2026-01-01T00:00:00Z"
	}
	var ver any
	if verification != "unverified" {
		ver = "2026-01-01T00:00:00Z"
	}
	e.sql(`INSERT INTO game_tag(game_id, tag_id, origin, qualifier, verification, verified_at, removed_at_source_at)
	       VALUES (?, ?, 'manual', ?, ?, ?, ?)`, gameID, id, qualifier, verification, ver, rem)
	return id
}

func (e *env) tagID(slug string) int64 {
	var id int64
	if err := e.raw.QueryRow(`SELECT id FROM tag WHERE slug = ?`, slug).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) updateResult(sourceID int64, at string) {
	e.t.Helper()
	e.sql(`INSERT INTO check_run(kind, started_at, finished_at, status) VALUES ('daily', ?, ?, 'ok')`, at, at)
	e.sql(`INSERT INTO check_result(run_id, source_id, step, outcome, old_key, new_key, at)
	       VALUES ((SELECT MAX(id) FROM check_run), ?, 'checker', 'update', '1', '2', ?)`, sourceID, at)
}

func (e *env) ids(f games.ListFilter) []int64 {
	e.t.Helper()
	rows, err := e.svc.List(ctx, f)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func eq(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func is(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func TestCreateConflicts(t *testing.T) {
	e := newEnv(t)
	f95 := games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: "123", URL: "https://f95zone.to/threads/123/"}
	first, err := e.svc.Create(ctx, games.CreateParams{Name: "A", Source: f95, LatestVersion: "v1", ChangeKey: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Game.PlayStatus != "planned" || first.Source.IsPrimary != 1 || first.Source.LatestVersion.String != "v1" {
		t.Fatalf("unexpected %+v", first)
	}
	itch := games.SourceSpec{Kind: domain.SourceItchio, ExternalID: "dev/game", URL: "https://dev.itch.io/game"}
	manual := games.SourceSpec{Kind: domain.SourceManual, URL: "https://example.com/g"}
	for _, sp := range []games.SourceSpec{itch, manual} {
		if _, err := e.svc.Create(ctx, games.CreateParams{Name: "B", Source: sp}); err != nil {
			t.Fatal(err)
		}
	}
	other := func(sp games.SourceSpec) games.SourceSpec { return sp }
	tests := []struct {
		name string
		spec games.SourceSpec
	}{
		{"f95 same thread id, different url", games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: "123", URL: "https://f95zone.to/threads/x.123/"}},
		{"itch same slug", other(games.SourceSpec{Kind: domain.SourceItchio, ExternalID: "dev/game", URL: "https://other.itch.io/x"})},
		{"itch same url, new slug", games.SourceSpec{Kind: domain.SourceItchio, ExternalID: "dev/game2", URL: "https://dev.itch.io/game"}},
		{"manual same url", manual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Create(ctx, games.CreateParams{Name: "dup", Source: tc.spec})
			is(t, err, domain.ErrConflict)
			var ce *games.ConflictError
			if !errors.As(err, &ce) || ce.GameID == 0 {
				t.Fatalf("no conflict game id in %v", err)
			}
		})
	}
	var ce *games.ConflictError
	_, err = e.svc.Create(ctx, games.CreateParams{Name: "dup", Source: f95})
	if !errors.As(err, &ce) || ce.GameID != first.Game.ID {
		t.Fatalf("conflict id = %v, want %d", err, first.Game.ID)
	}
	// F95 Sources are keyed by thread id only: same url under another id is fine.
	if _, err := e.svc.Create(ctx, games.CreateParams{Name: "ok", Source: games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: "124", URL: f95.URL}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.svc.List(ctx, games.ListFilter{})
	if len(rows) != 4 {
		t.Fatalf("conflicts left rows behind: %d games", len(rows))
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	good := games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: "1", URL: "https://f95zone.to/threads/1/"}
	tests := []struct {
		name string
		p    games.CreateParams
	}{
		{"empty name", games.CreateParams{Name: " ", Source: good}},
		{"bad kind", games.CreateParams{Name: "x", Source: games.SourceSpec{Kind: "rss", URL: good.URL}}},
		{"f95 without id", games.CreateParams{Name: "x", Source: games.SourceSpec{Kind: domain.SourceF95Thread, URL: good.URL}}},
		{"manual with id", games.CreateParams{Name: "x", Source: games.SourceSpec{Kind: domain.SourceManual, ExternalID: "1", URL: good.URL}}},
		{"bad url", games.CreateParams{Name: "x", Source: games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: "2", URL: "nope"}}},
		{"manual change key", games.CreateParams{Name: "x", Source: games.SourceSpec{Kind: domain.SourceManual, URL: good.URL}, ChangeKey: "k"}},
		{"bad play status", games.CreateParams{Name: "x", Source: good, PlayStatus: "done"}},
		{"bad rating", games.CreateParams{Name: "x", Source: good, RatingX2: 11}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Create(ctx, tc.p)
			is(t, err, domain.ErrValidation)
		})
	}
}

func TestPrimarySwitching(t *testing.T) {
	e := newEnv(t)
	g, err := e.svc.Create(ctx, games.CreateParams{
		Name: "A", DevStatus: domain.DevOngoing, LatestVersion: "v2", ChangeKey: "v2", ThreadUpdatedAt: "2026-02-01T00:00:00Z",
		Source: games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: "1", URL: "https://f95zone.to/threads/1/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.sql(`UPDATE source SET miss_count = 2, last_checked_at = '2026-02-02T00:00:00Z', details_pending = 1 WHERE id = ?`, g.Source.ID)
	e.sql(`INSERT INTO detail_fetch_queue(source_id, reason, budget, enqueued_at) VALUES (?, 'update', 'routine', '2026-02-02T00:00:00Z')`, g.Source.ID)
	link, err := e.svc.AddLinkSource(ctx, g.Game.ID, games.SourceSpec{Kind: domain.SourceItchio, ExternalID: "d/g", URL: "https://d.itch.io/g"})
	if err != nil {
		t.Fatal(err)
	}
	if link.IsPrimary != 0 {
		t.Fatal("link source must not be primary")
	}
	if _, err := e.svc.AddLinkSource(ctx, g.Game.ID, games.SourceSpec{Kind: domain.SourceItchio, ExternalID: "d/g", URL: "https://d.itch.io/g"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate link: %v", err)
	}
	e.sql(`UPDATE source SET genre_text = 'Harem, Vore' WHERE id = ?`, g.Source.ID)
	if err := e.svc.SetPrimary(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Detail(ctx, g.Game.ID)
	if err != nil {
		t.Fatal(err)
	}
	primaries := 0
	for _, s := range d.Sources {
		if s.IsPrimary == 1 {
			primaries++
			if s.ID != link.ID {
				t.Fatalf("wrong primary %d", s.ID)
			}
		} else if s.ID == g.Source.ID {
			if s.LatestVersion.Valid || s.ChangeKey.Valid || s.DevStatus.Valid || s.ThreadUpdatedAt.Valid ||
				s.MissCount != 0 || s.DetailsPending != 0 || s.LastCheckedAt.Valid || s.GenreText.Valid {
				t.Fatalf("old primary kept check fields: %+v", s)
			}
		}
	}
	if primaries != 1 || d.Primary.ID != link.ID || d.Sources[0].ID != link.ID {
		t.Fatalf("primaries=%d primary=%d", primaries, d.Primary.ID)
	}
	var queued int
	e.raw.QueryRow(`SELECT COUNT(*) FROM detail_fetch_queue`).Scan(&queued)
	if queued != 0 {
		t.Fatal("demoted source stayed queued")
	}
	// moving back and no-op on current primary
	if err := e.svc.SetPrimary(ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetPrimary(ctx, g.Source.ID); err != nil {
		t.Fatal(err)
	}
	is(t, e.svc.SetPrimary(ctx, 9999), domain.ErrNotFound)
}

func TestDevStatusRules(t *testing.T) {
	e := newEnv(t)
	f95, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{DevStatus: domain.DevOngoing})
	manual, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Kind: domain.SourceManual})
	itch, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Kind: domain.SourceItchio})
	done := domain.DevCompleted
	is(t, e.svc.SetDevStatus(ctx, f95.ID, &done), domain.ErrValidation)
	bad := domain.DevStatus("x")
	is(t, e.svc.SetDevStatus(ctx, manual.ID, &bad), domain.ErrValidation)
	for _, id := range []int64{manual.ID, itch.ID} {
		if err := e.svc.SetDevStatus(ctx, id, &done); err != nil {
			t.Fatal(err)
		}
		d, _ := e.svc.Detail(ctx, id)
		if d.Primary.DevStatus.String != "completed" {
			t.Fatalf("dev status = %v", d.Primary.DevStatus)
		}
		if err := e.svc.SetDevStatus(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		d, _ = e.svc.Detail(ctx, id)
		if d.Primary.DevStatus.Valid {
			t.Fatal("dev status not cleared")
		}
	}
	d, _ := e.svc.Detail(ctx, f95.ID)
	if d.Primary.DevStatus.String != "ongoing" {
		t.Fatal("F95 dev status changed")
	}
}

func TestReenableChecks(t *testing.T) {
	e := newEnv(t)
	g, src := testutil.InsertGame(t, e.store, testutil.GameSpec{LatestVersion: "v1"})
	e.sql(`UPDATE source SET unavailable_at = '2026-02-01T00:00:00Z', unavailable_reason = 'gone', miss_count = 3, checks_enabled = 0 WHERE id = ?`, src.ID)
	if err := e.svc.ReenableChecks(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	d, _ := e.svc.Detail(ctx, g.ID)
	if d.Primary.UnavailableAt.Valid || d.Primary.MissCount != 0 || d.Primary.ChecksEnabled != 1 {
		t.Fatalf("not re-enabled: %+v", d.Primary)
	}
}

func TestApplySourceDetail(t *testing.T) {
	e := newEnv(t)
	f95, fsrc := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Old", DevStatus: domain.DevOngoing, LatestVersion: "v1"})
	man, msrc := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Hand", Kind: domain.SourceManual, DevStatus: domain.DevOnHold})
	str := func(s string) *string { return &s }
	done := domain.DevCompleted
	at := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	apply := func(srcID int64, d games.Detail) (games.DetailResult, error) {
		var r games.DetailResult
		err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
			var err error
			r, err = e.svc.ApplySourceDetail(ctx, q, srcID, d)
			return err
		})
		return r, err
	}
	res, err := apply(fsrc.ID, games.Detail{Name: str("New Title"), LatestVersion: str("v2"), ChangeKey: str("v2"), DevStatus: &done,
		ThreadUpdatedAt: str("2026-03-01T00:00:00Z"), CoverURL: "https://img/x.jpg", DetailAt: at, DetailsPending: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.FetchCover || res.GameID != f95.ID {
		t.Fatalf("res = %+v", res)
	}
	d, _ := e.svc.Detail(ctx, f95.ID)
	if d.Game.Name != "New Title" || d.Primary.DevStatus.String != "completed" || d.Primary.LatestVersion.String != "v2" ||
		d.Primary.LastDetailAt.String != "2026-03-02T00:00:00Z" {
		t.Fatalf("not applied: %+v %+v", d.Game, d.Primary)
	}
	// Genre text is stored, then kept when a later detail has none.
	if _, err := apply(fsrc.ID, games.Detail{GenreText: str("Harem, NTR"), DetailAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(fsrc.ID, games.Detail{DetailAt: at}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.svc.Detail(ctx, f95.ID); d.Primary.GenreText.String != "Harem, NTR" {
		t.Fatalf("genre text = %q", d.Primary.GenreText.String)
	}
	// Only F95 Sources hold Genre text.
	if _, err := apply(msrc.ID, games.Detail{GenreText: str("x"), DetailAt: at}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.svc.Detail(ctx, man.ID); d.Primary.GenreText.Valid {
		t.Fatal("manual Source stored Genre text")
	}
	// omitted fields keep their values
	if _, err := apply(fsrc.ID, games.Detail{DetailAt: at}); err != nil {
		t.Fatal(err)
	}
	d, _ = e.svc.Detail(ctx, f95.ID)
	if d.Primary.LatestVersion.String != "v2" || d.Primary.DevStatus.String != "completed" {
		t.Fatalf("nil fields overwrote: %+v", d.Primary)
	}
	// Non-F95: user-owned Dev status and name are untouched.
	if _, err := apply(msrc.ID, games.Detail{Name: str("Other"), DevStatus: &done, DetailAt: at}); err != nil {
		t.Fatal(err)
	}
	d, _ = e.svc.Detail(ctx, man.ID)
	if d.Game.Name != "Hand" || d.Primary.DevStatus.String != "on_hold" {
		t.Fatalf("manual overwritten: %+v %+v", d.Game, d.Primary)
	}
	if _, err := apply(msrc.ID, games.Detail{ChangeKey: str("k"), DetailAt: at}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("manual change key: %v", err)
	}
	// Cover not needed once the same URL is cached and the file exists.
	e.sql(`UPDATE game SET cover_path = 'covers/x.jpg', cover_source_url = 'https://img/x.jpg' WHERE id = ?`, f95.ID)
	if res, _ := apply(fsrc.ID, games.Detail{CoverURL: "https://img/x.jpg", DetailAt: at}); !res.FetchCover {
		t.Fatal("missing cover file must trigger a fetch")
	}
	os.MkdirAll(filepath.Join(e.state, "covers"), 0o700)
	os.WriteFile(filepath.Join(e.state, "covers", "x.jpg"), []byte("x"), 0o600)
	if res, _ := apply(fsrc.ID, games.Detail{CoverURL: "https://img/x.jpg", DetailAt: at}); res.FetchCover {
		t.Fatal("unchanged cover refetched")
	}
	if res, _ := apply(fsrc.ID, games.Detail{CoverURL: "https://img/y.jpg", DetailAt: at}); !res.FetchCover {
		t.Fatal("changed cover url not refetched")
	}
	// Baseline writes versions without any check_result.
	if err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return e.svc.SetBaseline(ctx, q, fsrc.ID, "v9", "v9") }); err != nil {
		t.Fatal(err)
	}
	d, _ = e.svc.Detail(ctx, f95.ID)
	var n int
	e.raw.QueryRow(`SELECT COUNT(*) FROM check_result`).Scan(&n)
	if d.Primary.ChangeKey.String != "v9" || n != 0 || d.HasUpdate {
		t.Fatalf("baseline: %+v results=%d", d.Primary, n)
	}
}

func TestRating(t *testing.T) {
	e := newEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{})
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		stars   *float64
		wantX2  int64
		wantErr bool
	}{
		{f(0.5), 1, false}, {f(3), 6, false}, {f(4.5), 9, false}, {f(5), 10, false},
		{f(0), 0, true}, {f(5.5), 0, true}, {f(-1), 0, true}, {f(2.25), 0, true}, {f(0.3), 0, true},
		{nil, 0, false},
	}
	for _, tc := range tests {
		e.svc.SetRating(ctx, g.ID, f(4)) // known state before each case
		err := e.svc.SetRating(ctx, g.ID, tc.stars)
		d, _ := e.svc.Detail(ctx, g.ID)
		if tc.wantErr {
			is(t, err, domain.ErrValidation)
			if d.Game.RatingX2.Int64 != 8 {
				t.Fatalf("rejected rating changed stored value: %v", d.Game.RatingX2)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if d.Game.RatingX2.Int64 != tc.wantX2 || d.Game.RatingX2.Valid != (tc.stars != nil) {
			t.Fatalf("stars %v stored %v", tc.stars, d.Game.RatingX2)
		}
	}
	is(t, e.svc.SetRating(ctx, 999, f(3)), domain.ErrNotFound)
}

func TestPlayStatusPlatformImportReview(t *testing.T) {
	e := newEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{ImportReview: true})
	if err := e.svc.SetPlayStatus(ctx, g.ID, domain.PlayFinished); err != nil {
		t.Fatal(err)
	}
	is(t, e.svc.SetPlayStatus(ctx, g.ID, "bogus"), domain.ErrValidation)
	pp := domain.PlatformWin
	if err := e.svc.SetPlatformPref(ctx, g.ID, &pp); err != nil {
		t.Fatal(err)
	}
	bad := domain.PlatformPref("mac")
	is(t, e.svc.SetPlatformPref(ctx, g.ID, &bad), domain.ErrValidation)
	if err := e.svc.ClearImportReview(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	d, _ := e.svc.Detail(ctx, g.ID)
	if d.Game.PlayStatus != "finished" || d.Game.PlatformPref.String != "win" || d.Game.ImportReview != 0 {
		t.Fatalf("%+v", d.Game)
	}
	if err := e.svc.SetPlatformPref(ctx, g.ID, nil); err != nil {
		t.Fatal(err)
	}
	d, _ = e.svc.Detail(ctx, g.ID)
	if d.Game.PlatformPref.Valid {
		t.Fatal("platform pref not cleared")
	}
}

func lastPlayed(t *testing.T, e *env, id int64) (string, string) {
	t.Helper()
	d, err := e.svc.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.LastPlayed == nil {
		return "", ""
	}
	return d.LastPlayed.Version, d.LastPlayed.PlayedOn.String
}

func TestPlayLog(t *testing.T) {
	e := newEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{})
	if v, _ := lastPlayed(t, e, g.ID); v != "" {
		t.Fatal("empty log has last played")
	}
	testutil.InsertPlayLog(t, e.store, g.ID, "v0.5", "") // imported, undated
	if v, d := lastPlayed(t, e, g.ID); v != "v0.5" || d != "" {
		t.Fatalf("imported only: %q %q", v, d)
	}
	a, err := e.svc.AddPlayLog(ctx, g.ID, "v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.FirstUserEntry || a.Entry.PlayedOn.String != "2026-03-01" || a.Entry.Origin != "user" {
		t.Fatalf("first add: %+v", a)
	}
	if v, d := lastPlayed(t, e, g.ID); v != "v1" || d != "2026-03-01" {
		t.Fatalf("dated beats imported: %q %q", v, d)
	}
	b, err := e.svc.AddPlayLog(ctx, g.ID, "v0.8", "2026-02-01") // back-dated
	if err != nil {
		t.Fatal(err)
	}
	if b.FirstUserEntry {
		t.Fatal("second user entry flagged first")
	}
	if v, _ := lastPlayed(t, e, g.ID); v != "v1" {
		t.Fatalf("back-dated entry must not become last played, got %q", v)
	}
	// same date: higher id wins
	c, _ := e.svc.AddPlayLog(ctx, g.ID, "v1.1", "2026-03-01")
	if v, _ := lastPlayed(t, e, g.ID); v != "v1.1" {
		t.Fatalf("tie-break: %q", v)
	}
	// edit date back before the others: last played moves
	if _, err := e.svc.EditPlayLog(ctx, c.Entry.ID, " v1.1b ", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	if v, _ := lastPlayed(t, e, g.ID); v != "v1" {
		t.Fatalf("after edit: %q", v)
	}
	if err := e.svc.DeletePlayLog(ctx, a.Entry.ID); err != nil {
		t.Fatal(err)
	}
	if v, d := lastPlayed(t, e, g.ID); v != "v0.8" || d != "2026-02-01" {
		t.Fatalf("after delete: %q %q", v, d)
	}
	for _, id := range []int64{b.Entry.ID, c.Entry.ID} {
		e.svc.DeletePlayLog(ctx, id)
	}
	if v, d := lastPlayed(t, e, g.ID); v != "v0.5" || d != "" {
		t.Fatalf("falls back to imported: %q %q", v, d)
	}
	// validation
	_, err = e.svc.AddPlayLog(ctx, g.ID, "  ", "")
	is(t, err, domain.ErrValidation)
	_, err = e.svc.AddPlayLog(ctx, g.ID, "v2", "01/02/2026")
	is(t, err, domain.ErrValidation)
	_, err = e.svc.EditPlayLog(ctx, b.Entry.ID+100, "v", "2026-01-01")
	is(t, err, domain.ErrNotFound)
	user, _ := e.svc.AddPlayLog(ctx, g.ID, "v3", "")
	_, err = e.svc.EditPlayLog(ctx, user.Entry.ID, "v3", "")
	is(t, err, domain.ErrValidation)
	var imp int64
	e.raw.QueryRow(`SELECT id FROM play_log WHERE origin='imported'`).Scan(&imp)
	if got, err := e.svc.EditPlayLog(ctx, imp, "v0.6", ""); err != nil || got.PlayedOn.Valid || got.Version != "v0.6" {
		t.Fatalf("imported edit: %+v %v", got, err)
	}
}

func TestDeletePlayLogReferencedByTagReview(t *testing.T) {
	e := newEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{})
	pl := testutil.InsertPlayLog(t, e.store, g.ID, "v1", "2026-01-01")
	e.sql(`INSERT INTO tag_review(game_id, state, last_reviewed_play_log_id, updated_at) VALUES (?, 'pending', ?, '2026-01-01T00:00:00Z')`, g.ID, pl.ID)
	if err := e.svc.DeletePlayLog(ctx, pl.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Remove(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Detail(ctx, g.ID)
	is(t, err, domain.ErrNotFound)
	is(t, e.svc.Remove(ctx, g.ID), domain.ErrNotFound)
}

func TestBehindAndUpdateBadges(t *testing.T) {
	e := newEnv(t)
	g, src := testutil.InsertGame(t, e.store, testutil.GameSpec{PlayStatus: domain.PlayPlaying, LatestVersion: "v2"})
	flags := func() (alert, behind, upd bool) {
		d, err := e.svc.Detail(ctx, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := e.svc.List(ctx, games.ListFilter{})
		if rows[0].Behind == 1 != d.Behind || rows[0].HasUpdate == 1 != d.HasUpdate {
			t.Fatalf("list and detail disagree: %+v vs %+v", rows[0], d)
		}
		return d.Alerting, d.Behind, d.HasUpdate
	}
	if _, b, u := flags(); b || u {
		t.Fatal("empty play log must not be Behind")
	}
	testutil.InsertPlayLog(t, e.store, g.ID, "V1 ", "2026-01-05")
	if _, b, _ := flags(); !b {
		t.Fatal("v1 vs v2 should be Behind")
	}
	// the Update arrives after the play log entry was created (2026-01-01 in the fixture)
	e.updateResult(src.ID, "2026-02-01T00:00:00Z")
	if _, b, u := flags(); !b || !u {
		t.Fatalf("behind=%v update=%v", b, u)
	}
	// marking a version played (created_at = now > result) clears Update; still Behind until latest played
	if _, err := e.svc.AddPlayLog(ctx, g.ID, "v1", ""); err != nil {
		t.Fatal(err)
	}
	if _, b, u := flags(); !b || u {
		t.Fatalf("after marking played: behind=%v update=%v", b, u)
	}
	if _, err := e.svc.AddPlayLog(ctx, g.ID, "v2", ""); err != nil {
		t.Fatal(err)
	}
	if _, b, _ := flags(); b {
		t.Fatal("played latest: not Behind")
	}
	// Alert-set gating: finished is outside the default alert set.
	e.clk.Advance(48 * time.Hour)
	e.updateResult(src.ID, "2026-03-03T00:00:00Z")
	e.sql(`UPDATE source SET latest_version = 'v3'`)
	if a, b, u := flags(); !a || !b || !u {
		t.Fatalf("playing: %v %v %v", a, b, u)
	}
	if err := e.svc.SetPlayStatus(ctx, g.ID, domain.PlayFinished); err != nil {
		t.Fatal(err)
	}
	if a, b, u := flags(); a || b || u {
		t.Fatalf("finished must show no badges: %v %v %v", a, b, u)
	}
	if got := e.ids(games.ListFilter{Behind: true}); len(got) != 0 {
		t.Fatalf("behind filter leaked non-alert game: %v", got)
	}
	if err := e.svc.SetAlertSet(ctx, []domain.PlayStatus{domain.PlayFinished}); err != nil {
		t.Fatal(err)
	}
	if got := e.ids(games.ListFilter{Behind: true}); len(got) != 1 {
		t.Fatalf("alert set change ignored: %v", got)
	}
	// Dev-less, date-only source
	d, ds := testutil.InsertGame(t, e.store, testutil.GameSpec{Kind: domain.SourceItchio, PlayStatus: domain.PlayFinished, ThreadUpdatedAt: "2026-03-01T00:00:00Z"})
	_ = ds
	testutil.InsertPlayLog(t, e.store, d.ID, "whatever", "2026-02-01")
	if got := e.ids(games.ListFilter{Behind: true}); len(got) != 2 {
		t.Fatalf("date-only behind: %v", got)
	}
}

func TestAlertSetAndCounts(t *testing.T) {
	e := newEnv(t)
	for _, ps := range []domain.PlayStatus{domain.PlayPlaying, domain.PlayPlaying, domain.PlayDropped} {
		testutil.InsertGame(t, e.store, testutil.GameSpec{PlayStatus: ps})
	}
	set, _ := e.svc.AlertSet(ctx)
	if len(set) != 3 {
		t.Fatalf("default alert set = %v", set)
	}
	is(t, e.svc.SetAlertSet(ctx, []domain.PlayStatus{"x"}), domain.ErrValidation)
	if err := e.svc.SetAlertSet(ctx, []domain.PlayStatus{domain.PlayDropped, domain.PlayDropped, domain.PlayFinished}); err != nil {
		t.Fatal(err)
	}
	set, _ = e.svc.AlertSet(ctx)
	if len(set) != 2 || set[0] != domain.PlayDropped || set[1] != domain.PlayFinished {
		t.Fatalf("alert set = %v", set)
	}
	c, err := e.svc.PlayStatusCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c[domain.PlayPlaying] != 2 || c[domain.PlayDropped] != 1 || c[domain.PlayPlanned] != 0 || len(c) != 5 {
		t.Fatalf("counts = %v", c)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	s, err := e.svc.Settings(ctx)
	if err != nil || s.PlatformPref != domain.PlatformLinux || len(s.MirrorHostOrder) != 3 || s.MirrorHostOrder[0] != "pixeldrain" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	want := games.Settings{PlatformPref: domain.PlatformWin, MirrorHostOrder: []string{"gofile", "mega"}}
	if err := e.svc.SetSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	s, _ = e.svc.Settings(ctx)
	if s.PlatformPref != "win" || strings.Join(s.MirrorHostOrder, ",") != "gofile,mega" {
		t.Fatalf("%+v", s)
	}
	is(t, e.svc.SetSettings(ctx, games.Settings{PlatformPref: "x"}), domain.ErrValidation)
	is(t, e.svc.SetSettings(ctx, games.Settings{PlatformPref: "win", MirrorHostOrder: []string{"a", "a"}}), domain.ErrValidation)
}

func TestListFiltersAndSorts(t *testing.T) {
	e := newEnv(t)
	at := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	// ids in insertion order
	alpha, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha Quest", PlayStatus: domain.PlayPlaying, RatingX2: 8, DevStatus: domain.DevOngoing, ThreadUpdatedAt: "2026-02-01T00:00:00Z", At: at(1)})
	beta, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "beta_100%", PlayStatus: domain.PlayFinished, RatingX2: 4, DevStatus: domain.DevCompleted, ThreadUpdatedAt: "2026-02-03T00:00:00Z", At: at(2)})
	gamma, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Gamma", PlayStatus: domain.PlayPlanned, ThreadUpdatedAt: "2026-02-02T00:00:00Z", At: at(3)})
	delta, dsrc := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Delta", PlayStatus: domain.PlayDropped, RatingX2: 10, DevStatus: domain.DevAbandoned, At: at(4)})
	_ = dsrc
	testutil.InsertPlayLog(t, e.store, alpha.ID, "v1", "2026-02-10")
	testutil.InsertPlayLog(t, e.store, gamma.ID, "v1", "2026-02-20")
	testutil.InsertPlayLog(t, e.store, beta.ID, "v1", "")
	A, B, C, D := alpha.ID, beta.ID, gamma.ID, delta.ID

	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			name string
			f    games.ListFilter
			want []int64 // as returned by default sort check below via set equality
		}{
			{"all", games.ListFilter{}, []int64{A, B, C, D}},
			{"name substring ci", games.ListFilter{Name: "QUEST"}, []int64{A}},
			{"name with sql wildcard is literal", games.ListFilter{Name: "100%"}, []int64{B}},
			{"name underscore literal", games.ListFilter{Name: "a_1"}, []int64{B}},
			{"play status", games.ListFilter{PlayStatus: domain.PlayPlanned}, []int64{C}},
			{"dev status", games.ListFilter{DevStatus: domain.DevAbandoned}, []int64{D}},
			{"min rating", games.ListFilter{MinRatingX2: 8}, []int64{A, D}},
			{"no match", games.ListFilter{Name: "zzz"}, nil},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := e.ids(tc.f)
				if len(got) != len(tc.want) {
					t.Fatalf("got %v want %v", got, tc.want)
				}
				seen := map[int64]bool{}
				for _, id := range got {
					seen[id] = true
				}
				for _, id := range tc.want {
					if !seen[id] {
						t.Fatalf("got %v want %v", got, tc.want)
					}
				}
			})
		}
		is(t, func() error { _, err := e.svc.List(ctx, games.ListFilter{PlayStatus: "x"}); return err }(), domain.ErrValidation)
		is(t, func() error { _, err := e.svc.List(ctx, games.ListFilter{Sort: "size"}); return err }(), domain.ErrValidation)
	})

	t.Run("sorts", func(t *testing.T) {
		yes := true
		no := false
		tests := []struct {
			name string
			f    games.ListFilter
			want []int64
		}{
			{"default: updated at source desc, no-date last", games.ListFilter{}, []int64{B, C, A, D}},
			{"name asc", games.ListFilter{Sort: games.SortName}, []int64{A, B, D, C}},
			{"name desc", games.ListFilter{Sort: games.SortName, Desc: &yes}, []int64{C, D, B, A}},
			{"rating desc, unrated last", games.ListFilter{Sort: games.SortRating}, []int64{D, A, B, C}},
			{"rating asc, unrated last", games.ListFilter{Sort: games.SortRating, Desc: &no}, []int64{B, A, D, C}},
			{"play status", games.ListFilter{Sort: games.SortPlayStatus}, []int64{A, C, B, D}},
			{"last played desc, undated/never last", games.ListFilter{Sort: games.SortLastPlayed}, []int64{C, A, D, B}},
			{"last played asc", games.ListFilter{Sort: games.SortLastPlayed, Desc: &no}, []int64{A, C, D, B}},
			{"added desc", games.ListFilter{Sort: games.SortAdded}, []int64{D, C, B, A}},
			{"added asc", games.ListFilter{Sort: games.SortAdded, Desc: &no}, []int64{A, B, C, D}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) { eq(t, e.ids(tc.f), tc.want) })
		}
	})

	t.Run("default puts Update/Behind first", func(t *testing.T) {
		// alpha playing + latest v2 vs played v1 = Behind; beta is finished (no badge)
		e.sql(`UPDATE source SET latest_version = 'v2' WHERE game_id = ?`, A)
		eq(t, e.ids(games.ListFilter{}), []int64{A, B, C, D})
		eq(t, e.ids(games.ListFilter{Behind: true}), []int64{A})
		e.updateResult(func() int64 {
			var id int64
			e.raw.QueryRow(`SELECT id FROM source WHERE game_id=?`, C).Scan(&id)
			return id
		}(), "2026-02-25T00:00:00Z")
		eq(t, e.ids(games.ListFilter{Updates: true}), []int64{C})
		eq(t, e.ids(games.ListFilter{}), []int64{C, A, B, D})
	})
}

func TestTagFilters(t *testing.T) {
	e := newEnv(t)
	mk := func(name string) int64 {
		g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: name})
		return g.ID
	}
	g1, g2, g3, g4, g5 := mk("g1"), mk("g2"), mk("g3"), mk("g4"), mk("g5")
	e.tag(g1, "rpg", "present", "unverified", false)
	e.tag(g1, "fantasy", "present", "confirmed", false)
	e.tag(g2, "rpg", "present", "confirmed", false)
	e.tag(g3, "rpg", "planned", "unverified", false)      // planned: only with AllQualifiers
	e.tag(g3, "fantasy", "optional", "unverified", false) // optional
	e.tag(g4, "rpg", "present", "wrong", false)           // wrong never matches
	e.tag(g4, "fantasy", "present", "unverified", false)
	e.tag(g5, "rpg", "present", "unverified", true) // removed at source never matches
	rpg, fantasy := e.tagID("rpg"), e.tagID("fantasy")
	tests := []struct {
		name string
		f    games.ListFilter
		want []int64
	}{
		{"include one", games.ListFilter{IncludeTags: []int64{rpg}}, []int64{g2, g1}},
		{"include AND", games.ListFilter{IncludeTags: []int64{rpg, fantasy}}, []int64{g1}},
		{"exclude", games.ListFilter{ExcludeTags: []int64{rpg}}, []int64{g5, g4, g3}},
		{"include and exclude", games.ListFilter{IncludeTags: []int64{rpg}, ExcludeTags: []int64{fantasy}}, []int64{g2}},
		{"all qualifiers include", games.ListFilter{IncludeTags: []int64{rpg}, AllQualifiers: true}, []int64{g3, g2, g1}},
		{"all qualifiers AND", games.ListFilter{IncludeTags: []int64{rpg, fantasy}, AllQualifiers: true}, []int64{g3, g1}},
		{"all qualifiers exclude", games.ListFilter{ExcludeTags: []int64{rpg}, AllQualifiers: true}, []int64{g5, g4}},
		{"exclude ignores wrong tag", games.ListFilter{ExcludeTags: []int64{fantasy}}, []int64{g5, g3, g2}},
		{"unknown tag include matches nothing", games.ListFilter{IncludeTags: []int64{99999}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			f.Sort = games.SortAdded // all equal added_at: id desc tie-break
			eq(t, e.ids(f), tc.want)
		})
	}
}

func TestCover(t *testing.T) {
	e := newEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{})
	png := []byte("\x89PNG....")
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	mux.HandleFunc("/new.webp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp; charset=binary")
		w.Write([]byte("webp"))
	})
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>"))
	})
	mux.HandleFunc("/big.jpg", func(w http.ResponseWriter, r *http.Request) { // unknown length: cap enforced while reading
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Transfer-Encoding", "chunked")
		chunk := make([]byte, 1<<20)
		for i := 0; i < 11; i++ {
			w.Write(chunk)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/bigcl.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", "20000000")
	})
	mux.HandleFunc("/missing.png", http.NotFound)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := e.svc.CoverPath(ctx, g.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no cover yet: %v", err)
	}
	if err := e.svc.FetchCover(ctx, g.ID, srv.URL+"/ok.png"); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.CoverPath(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(png) || filepath.Base(p) != fmtID(g.ID)+".png" {
		t.Fatalf("stored %q at %s", got, p)
	}
	d, _ := e.svc.Detail(ctx, g.ID)
	if d.Game.CoverPath.String != "covers/"+fmtID(g.ID)+".png" || d.Game.CoverSourceUrl.String != srv.URL+"/ok.png" ||
		d.Game.CoverFetchedAt.String != "2026-03-01T12:00:00Z" {
		t.Fatalf("columns: %+v", d.Game)
	}
	for _, path := range []string{"/page.html", "/big.jpg", "/bigcl.jpg", "/missing.png"} {
		err := e.svc.FetchCover(ctx, g.ID, srv.URL+path)
		if !errors.Is(err, games.ErrCoverRejected) {
			t.Fatalf("%s: %v", path, err)
		}
		d, _ = e.svc.Detail(ctx, g.ID)
		if got, _ := os.ReadFile(p); string(got) != string(png) || d.Game.CoverSourceUrl.String != srv.URL+"/ok.png" {
			t.Fatalf("%s: old cover not kept", path)
		}
	}
	if err := e.svc.FetchCover(ctx, g.ID, "http://127.0.0.1:1/x.png"); err == nil {
		t.Fatal("unreachable host should fail")
	}
	// A new image replaces the old file even when the extension differs.
	if err := e.svc.FetchCover(ctx, g.ID, srv.URL+"/new.webp"); err != nil {
		t.Fatal(err)
	}
	p2, _ := e.svc.CoverPath(ctx, g.ID)
	if filepath.Base(p2) != fmtID(g.ID)+".webp" {
		t.Fatal(p2)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("old cover file left behind")
	}
	entries, _ := os.ReadDir(filepath.Join(e.state, "covers"))
	if len(entries) != 1 {
		t.Fatalf("temp files left in covers dir: %v", entries)
	}
	if err := e.svc.Remove(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Fatal("cover survives Game removal")
	}
}

func fmtID(id int64) string { return strconv.FormatInt(id, 10) }
