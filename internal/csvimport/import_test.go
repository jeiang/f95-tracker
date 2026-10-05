package csvimport

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/testutil"
	_ "modernc.org/sqlite"
)

var ctx = context.Background()

const fixtureDir = "../../testdata/f95"

type env struct {
	t     *testing.T
	store *db.Store
	raw   *sql.DB
	fake  *testutil.F95Fake
	creds *f95.CredStore
	im    *Importer
	lines int // sheet lines to import (header included); 0 = all
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newEnv serves every F95 thread of the sheet as the ongoing fixture, checker.php
// answers "v9" for each, and a valid cookie is stored.
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

	clk := clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC))
	e := &env{t: t, store: store, raw: raw, fake: testutil.NewF95Fake(t), creds: f95.NewCredStore(store, clk)}
	e.login()
	sheet := e.rows()
	for _, r := range sheet {
		if r.F95() {
			e.serve(r.Spec.ExternalID, "67494.html")
			e.fake.SetVersion(r.Spec.ExternalID, "v9")
		}
	}
	fc, err := f95.New(f95.Options{
		BaseURL: e.fake.URL(), Creds: e.creds, Version: "test",
		Pacer: &f95.Pacer{LockPath: filepath.Join(dir, "f95.lock"), Spacing: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	gs := games.New(store, clk, games.Options{StateDir: filepath.Join(dir, "state")})
	ts := tags.New(store, clk)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.im = &Importer{
		Store: store, Clock: clk, Games: gs, Tags: ts, F95: fc, Log: log,
		Refresher: check.NewRefresher(store, clk, gs, ts, fc, itch.NewClient(itch.Options{Clock: clk}), log),
	}
	return e
}

func (e *env) login() {
	e.t.Helper()
	if err := e.creds.Replace(ctx, f95.Jar{"xf_user": "user1", "xf_tfa_trust": "t"}, "TestBrowser/1.0", time.Time{}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) serve(id, fixture string) {
	e.fake.SetThread(id, readFixture(e.t, fixture), readFixture(e.t, "59416.guest.html"))
}

func (e *env) rows() []Row {
	e.t.Helper()
	f, err := os.Open("testdata/sheet.csv")
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	rows, err := Parse(f)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) run(backfill bool) (Summary, error) {
	e.t.Helper()
	f, err := os.Open("testdata/sheet.csv")
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	var r io.Reader = f
	if e.lines > 0 {
		b, _ := io.ReadAll(f)
		r = strings.NewReader(strings.Join(strings.SplitAfter(string(b), "\n")[:e.lines], ""))
	}
	return e.im.Run(ctx, r, backfill)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.raw.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// game returns the Game and primary Source of a thread (kind f95_thread) or of a link.
func (e *env) game(thread string) (sqlcgen.Game, sqlcgen.Source) {
	e.t.Helper()
	q := e.store.Queries()
	var src sqlcgen.Source
	var err error
	if strings.HasPrefix(thread, "http") {
		for _, kind := range []domain.SourceKind{domain.SourceItchio, domain.SourceManual} {
			if src, err = q.FindSourceByURL(ctx, sqlcgen.FindSourceByURLParams{Kind: string(kind), Url: thread}); err == nil {
				break
			}
		}
	} else {
		src, err = q.FindSourceByExternal(ctx, sqlcgen.FindSourceByExternalParams{Kind: "f95_thread", ExternalID: sql.NullString{String: thread, Valid: true}})
		if errors.Is(err, sql.ErrNoRows) { // restricted threads are manual Sources
			src, err = q.FindSourceByURL(ctx, sqlcgen.FindSourceByURLParams{Kind: "manual", Url: F95Base + "/threads/" + thread + "/"})
		}
	}
	if err != nil {
		e.t.Fatalf("find %s: %v", thread, err)
	}
	g, err := q.GetGame(ctx, src.GameID)
	if err != nil {
		e.t.Fatal(err)
	}
	return g, src
}

func (e *env) versions(gameID int64) []string {
	e.t.Helper()
	log, err := e.store.Queries().ListPlayLog(ctx, gameID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for i := len(log) - 1; i >= 0; i-- { // oldest first
		if log[i].Origin != "imported" || log[i].PlayedOn.Valid {
			e.t.Errorf("play log %+v must be imported and undated", log[i])
		}
		out = append(out, log[i].Version)
	}
	return out
}

func TestImportSheet(t *testing.T) {
	e := newEnv(t)
	sum, err := e.run(false)
	if err != nil {
		t.Fatal(err)
	}
	want := Summary{Rows: 187, Games: 184, Merged: 3, Converted: 2, Baselined: 178, BackfillRemaining: 178}
	if sum != want {
		t.Fatalf("summary = %+v, want %+v", sum, want)
	}
	if n := e.count(`SELECT COUNT(*) FROM game`); n != 184 {
		t.Fatalf("games = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM game WHERE import_review = 1`); n != 184 {
		t.Fatalf("import_review games = %d", n)
	}

	t.Run("merges", func(t *testing.T) {
		for _, c := range []struct {
			thread   string
			versions []string
			rating   int64
		}{
			{"64303", []string{"0.7", "Day 3"}, 7},   // newer Rework row supplies rating 3.5
			{"70143", []string{"0.7.3", "0.1.1"}, 8}, // Remake 4 stars
			{"67937", []string{"1.0", "1.00D1"}, 9},
			{"178717", []string{"Ep. 3"}, 9}, // stays separate
		} {
			g, _ := e.game(c.thread)
			if got := e.versions(g.ID); strings.Join(got, "|") != strings.Join(c.versions, "|") {
				t.Errorf("%s play log = %v, want %v", c.thread, got, c.versions)
			}
			if g.RatingX2.Int64 != c.rating {
				t.Errorf("%s rating_x2 = %d, want %d", c.thread, g.RatingX2.Int64, c.rating)
			}
		}
		a, _ := e.game("67937")
		b, _ := e.game("178717")
		if a.ID == b.ID {
			t.Error("178717 merged into 67937")
		}
		if g, _ := e.game("67937"); g.Name != "My Office Adventures" {
			t.Errorf("merged Game name = %q, want the older row's", g.Name)
		}
	})

	t.Run("restricted threads become manual", func(t *testing.T) {
		for id, want := range map[string]struct {
			name string
			dev  domain.DevStatus
		}{"94891": {"Lycoris Radiata", domain.DevAbandoned}, "151517": {"Ovulating Maiden", domain.DevAbandoned}} {
			g, src := e.game(id)
			if src.Kind != "manual" || src.ExternalID.Valid || src.Url != "https://f95zone.to/threads/"+id+"/" || src.DevStatus.String != string(want.dev) || src.IsPrimary != 1 {
				t.Errorf("%s source = %+v", id, src)
			}
			if g.Name != want.name || g.PlayStatus != "finished" {
				t.Errorf("%s game = %+v", id, g)
			}
			if e.count(`SELECT COUNT(*) FROM tag_review WHERE game_id = ?`, g.ID) != 0 {
				t.Errorf("%s: manual Game got a tag_review", id)
			}
		}
		if n := e.count(`SELECT COUNT(*) FROM source WHERE kind = 'manual'`); n != 3 {
			t.Errorf("manual sources = %d, want 2 restricted + fanbox", n)
		}
	})

	t.Run("itch.io and fanbox rows", func(t *testing.T) {
		_, rework := e.game("https://kuro-kai.itch.io/lycoris-radiata")
		_, second := e.game("https://abbys-cat.itch.io/my-new-second-chance")
		_, alchemy := e.game("https://jjambong.itch.io/alchemy-shop")
		for _, s := range []sqlcgen.Source{rework, second, alchemy} {
			if s.Kind != "itchio" || !s.ExternalID.Valid || s.ChangeKey.Valid || s.LatestVersion.Valid {
				t.Errorf("itch source = %+v", s)
			}
		}
		if rework.ChecksEnabled != 1 || second.ChecksEnabled != 1 || alchemy.ChecksEnabled != 0 {
			t.Errorf("checks enabled: %d %d %d, want only Alchemy Shop off", rework.ChecksEnabled, second.ChecksEnabled, alchemy.ChecksEnabled)
		}
		g, fanbox := e.game("https://directorgames.fanbox.cc/")
		if fanbox.Kind != "manual" || fanbox.DevStatus.String != "ongoing" || g.Name != "Little Green Hill" {
			t.Errorf("fanbox = %+v %+v", g, fanbox)
		}
		// Alchemy Shop: Dev status Completed in the sheet, a Demo version → finished.
		if ag, _ := e.game("https://jjambong.itch.io/alchemy-shop"); ag.PlayStatus != "finished" || alchemy.DevStatus.String != "completed" {
			t.Errorf("alchemy = %+v / %q", ag, alchemy.DevStatus.String)
		}
	})

	t.Run("provisional Play status and tag review", func(t *testing.T) {
		for thread, want := range map[string]string{
			"254874": "planned",  // version "-"
			"31912":  "playing",  // ongoing
			"59416":  "playing",  // On Hold is Dev status
			"114650": "finished", // Completed
			"92250":  "finished", // Abandoned (flag stale; backfill re-derives)
		} {
			g, src := e.game(thread)
			if g.PlayStatus != want || g.ImportReview != 1 {
				t.Errorf("%s play status = %q review=%d, want %q", thread, g.PlayStatus, g.ImportReview, want)
			}
			if src.DevStatus.Valid {
				t.Errorf("%s: F95 Dev status must wait for the live fetch, got %q", thread, src.DevStatus.String)
			}
			if n := e.count(`SELECT COUNT(*) FROM tag_review WHERE game_id = ? AND state = 'pending'`, g.ID); n != 1 {
				t.Errorf("%s tag_review pending = %d", thread, n)
			}
		}
		if g, _ := e.game("254874"); len(e.versions(g.ID)) != 0 || g.RatingX2.Valid {
			t.Error("Wartribe Alliances has no version and no rating")
		}
		if n := e.count(`SELECT COUNT(*) FROM tag_review`); n != 178 {
			t.Errorf("tag_review rows = %d, want one per F95 Game", n)
		}
	})

	t.Run("ratings are doubled", func(t *testing.T) {
		for thread, want := range map[string]int64{"31912": 10, "50488": 10, "59416": 10} {
			if g, _ := e.game(thread); g.RatingX2.Int64 != want {
				t.Errorf("%s rating_x2 = %d, want %d", thread, g.RatingX2.Int64, want)
			}
		}
		if n := e.count(`SELECT COUNT(*) FROM game WHERE rating_x2 IS NULL`); n != 2 {
			t.Errorf("unrated games = %d, want 2 (the '-' rating cells)", n)
		}
	})

	t.Run("baseline without check_result", func(t *testing.T) {
		_, src := e.game("67494")
		if src.LatestVersion.String != "v9" || src.ChangeKey.String != "v9" || src.LastCheckedAt.Valid {
			t.Errorf("baseline source = %+v", src)
		}
		if n := e.count(`SELECT COUNT(*) FROM check_result`); n != 0 {
			t.Errorf("check_result rows = %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM detail_fetch_queue`); n != 0 {
			t.Errorf("queue = %d with --no-backfill", n)
		}
	})

	t.Run("second run changes nothing", func(t *testing.T) {
		before := e.count(`SELECT COUNT(*) FROM play_log`)
		sum, err := e.run(false)
		if err != nil {
			t.Fatal(err)
		}
		want := Summary{Rows: 187, Skipped: 187, BackfillRemaining: 178}
		if sum != want {
			t.Fatalf("summary = %+v, want %+v", sum, want)
		}
		if n := e.count(`SELECT COUNT(*) FROM game`); n != 184 {
			t.Errorf("games = %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM play_log`); n != before {
			t.Errorf("play_log %d -> %d", before, n)
		}
	})
}

func TestBackfillPausesAndResumes(t *testing.T) {
	e := newEnv(t)
	e.lines = 20 // the backfill is slow; the first rows hold F95, converted and itch.io rows
	// Third F95 row of the sheet (31912): F95 answers with the logged-out page.
	e.fake.Program("31912", testutil.F95Response{Body: string(readFixture(t, "59416.guest.html"))})

	sum, err := e.run(true)
	if !errors.Is(err, ErrPaused) || !errors.Is(err, f95.ErrCookieInvalid) {
		t.Fatalf("err = %v, want paused on invalid cookie", err)
	}
	n := e.count(`SELECT COUNT(*) FROM source WHERE kind = 'f95_thread'`)
	if sum.BackfillDone != 2 || sum.BackfillRemaining != n-2 || n < 10 {
		t.Fatalf("summary = %+v, f95 games %d", sum, n)
	}
	if q := e.count(`SELECT COUNT(*) FROM detail_fetch_queue WHERE reason = 'import' AND budget = 'import'`); q != n-2 {
		t.Fatalf("queue = %d, want the unfetched Games kept", n)
	}
	var status string
	var stopped int
	if err := e.raw.QueryRow(`SELECT status, f95_stopped FROM check_run WHERE kind = 'import_backfill'`).Scan(&status, &stopped); err != nil || status != "partial" || stopped != 1 {
		t.Fatalf("run = %q stopped=%d err=%v", status, stopped, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM check_result WHERE outcome = 'fetched'`); n != 2 {
		t.Fatalf("fetched results = %d", n)
	}

	e.login() // the user pasted a fresh cookie
	sum, err = e.run(true)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Games != 0 || sum.Skipped != sum.Rows || sum.BackfillDone != n-2 || sum.BackfillRemaining != 0 {
		t.Fatalf("resume summary = %+v", sum)
	}
	if n := e.count(`SELECT COUNT(*) FROM detail_fetch_queue`); n != 0 {
		t.Fatalf("queue = %d after resume", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM check_run WHERE kind = 'import_backfill'`); n != 2 {
		t.Fatalf("backfill runs = %d", n)
	}
	// Baseline survived: the fetch only seeds versions a Source lacks, and no Update was recorded.
	if n := e.count(`SELECT COUNT(*) FROM check_result WHERE outcome = 'update'`); n != 0 {
		t.Fatalf("updates = %d", n)
	}
	g, src := e.game("67494")
	if g.Name != "Out of Touch!" || src.LatestVersion.String != "v9" || src.DevStatus.String != "ongoing" {
		t.Fatalf("after backfill: %+v %+v", g, src)
	}
	if n := e.count(`SELECT COUNT(*) FROM game_tag gt JOIN game g ON g.id = gt.game_id WHERE gt.verification <> 'unverified'`); n != 0 {
		t.Fatalf("verified tags after import = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM game_tag`); n == 0 {
		t.Fatal("backfill stored no tags")
	}
}

func TestBackfillPausesOnBlock(t *testing.T) {
	e := newEnv(t)
	e.lines = 20
	e.fake.Program("254874", testutil.F95Challenge())
	sum, err := e.run(true)
	if !errors.Is(err, ErrPaused) || !errors.Is(err, f95.ErrBlocked) {
		t.Fatalf("err = %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM source WHERE kind = 'f95_thread'`); sum.BackfillDone != 1 || sum.BackfillRemaining != n-1 {
		t.Fatalf("summary = %+v, f95 games %d", sum, n)
	}
}

func TestBackfillRederivesOnlyWhileImportReview(t *testing.T) {
	e := newEnv(t)
	e.lines = 20
	if _, err := e.run(false); err != nil {
		t.Fatal(err)
	}
	e.serve("114650", "67426.html") // live: on hold   (sheet: Completed → finished)
	e.serve("50488", "67426.html")  // live: on hold   (sheet: Completed → finished)
	e.serve("92250", "67494.html")  // live: ongoing   (sheet: Abandoned → finished)
	e.serve("59416", "114650.html") // live: completed (sheet: On Hold → playing)
	confirmed, _ := e.game("50488")
	if err := e.im.Games.ClearImportReview(ctx, confirmed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(true); err != nil {
		t.Fatal(err)
	}
	for thread, want := range map[string]string{
		"114650": "playing",  // re-derived
		"92250":  "playing",  // Furina: flag stale
		"59416":  "finished", // live completed
		"50488":  "finished", // confirmed by the user: left alone
		"254874": "planned",  // no version stays planned whatever the live Dev status
	} {
		if g, _ := e.game(thread); g.PlayStatus != want {
			t.Errorf("%s play status = %q, want %q", thread, g.PlayStatus, want)
		}
	}
}

func TestDerivePlayStatus(t *testing.T) {
	for _, c := range []struct {
		played bool
		dev    domain.DevStatus
		want   domain.PlayStatus
	}{
		{false, domain.DevOngoing, domain.PlayPlanned},
		{false, domain.DevCompleted, domain.PlayPlanned},
		{true, domain.DevCompleted, domain.PlayFinished},
		{true, domain.DevAbandoned, domain.PlayFinished},
		{true, domain.DevOngoing, domain.PlayPlaying},
		{true, domain.DevOnHold, domain.PlayPlaying},
	} {
		if got := derivePlayStatus(c.played, c.dev); got != c.want {
			t.Errorf("derive(%v, %s) = %s, want %s", c.played, c.dev, got, c.want)
		}
	}
}

func TestParseClassification(t *testing.T) {
	rows, err := Parse(strings.NewReader("Name,Version,Abandoned,Completed,On Hold,Rating,URL Code,Link\n" +
		"A,1.0,FALSE,FALSE,FALSE,3.5,12,https://f95zone.to/threads/12/\n" +
		"B,-,FALSE,FALSE,TRUE,-,,https://f95zone.to/threads/some-name.77/\n" +
		"C,2,TRUE,FALSE,FALSE,1,151517,https://f95zone.to/threads/151517/\n" +
		"D,-,FALSE,FALSE,FALSE,-,,https://x.itch.io/d-game\n" +
		"E,-,FALSE,FALSE,FALSE,-,,https://dev.fanbox.cc/\n"))
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		kind  domain.SourceKind
		ext   string
		dev   domain.DevStatus
		x2    int
		ver   string
		conv  bool
		thURL string
	}
	var have []got
	for _, r := range rows {
		have = append(have, got{r.Spec.Kind, r.Spec.ExternalID, r.DevStatus, r.RatingX2, r.Version, r.Converted, r.Spec.URL})
	}
	want := []got{
		{"f95_thread", "12", "ongoing", 7, "1.0", false, "https://f95zone.to/threads/12/"},
		{"f95_thread", "77", "on_hold", 0, "", false, "https://f95zone.to/threads/77/"},
		{"manual", "", "abandoned", 2, "2", true, "https://f95zone.to/threads/151517/"},
		{"itchio", "d-game", "ongoing", 0, "", false, "https://x.itch.io/d-game"},
		{"manual", "", "ongoing", 0, "", false, "https://dev.fanbox.cc/"},
	}
	for i := range want {
		if have[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, have[i], want[i])
		}
	}
}
