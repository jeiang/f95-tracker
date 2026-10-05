package check

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// coverTransport serves every cover URL from memory: status 200 with a PNG, or the programmed status.
type coverTransport struct{ status int }

func (c *coverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h := http.Header{"Content-Type": {"image/png"}}
	return &http.Response{StatusCode: c.status, Header: h, Body: io.NopCloser(bytes.NewReader([]byte("\x89PNG fake"))), Request: req}, nil
}

type env struct {
	t      *testing.T
	store  *db.Store
	raw    *sql.DB
	clk    *clock.Fake
	fake   *testutil.F95Fake
	ifake  *testutil.ItchFake
	creds  *f95.CredStore
	cover  *coverTransport
	state  string
	ref    *Refresher
	tagsvc *tags.Service
}

func newEnv(t *testing.T, withCookie bool) *env {
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
	e := &env{t: t, store: store, raw: raw, clk: clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)),
		fake: testutil.NewF95Fake(t), ifake: testutil.NewItchFake(t), cover: &coverTransport{status: 200}, state: filepath.Join(dir, "state")}
	e.creds = f95.NewCredStore(store, e.clk)
	if withCookie {
		if err := e.creds.Replace(ctx, f95.Jar{"xf_user": "user1", "xf_tfa_trust": "t"}, "TestBrowser/1.0", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	fc, err := f95.New(f95.Options{BaseURL: e.fake.URL(), Creds: e.creds, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	ic := itch.NewClient(itch.Options{Clock: e.clk, BaseURL: e.ifake.URL})
	g := games.New(store, e.clk, games.Options{StateDir: e.state, HTTPClient: &http.Client{Transport: e.cover}})
	e.tagsvc = tags.New(store, e.clk)
	e.ref = NewRefresher(store, e.clk, g, e.tagsvc, fc, ic, nil)
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.raw.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) run() int64 {
	e.t.Helper()
	id, finish, err := e.ref.StartManualRun(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { finish("ok") })
	return id
}

func (e *env) enqueue(sourceID int64) {
	e.exec(`INSERT INTO detail_fetch_queue(source_id, reason, budget, enqueued_at) VALUES (?, 'manual', 'routine', '2026-10-01T00:00:00Z')`, sourceID)
}

func (e *env) queued(sourceID int64) bool {
	var n int
	if err := e.raw.QueryRow(`SELECT COUNT(*) FROM detail_fetch_queue WHERE source_id = ?`, sourceID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n == 1
}

func (e *env) results(sourceID int64) []sqlcgen.CheckResult {
	e.t.Helper()
	rows, err := e.raw.Query(`SELECT outcome, step, old_key, new_key, error FROM check_result WHERE source_id = ? ORDER BY id`, sourceID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []sqlcgen.CheckResult
	for rows.Next() {
		var r sqlcgen.CheckResult
		if err := rows.Scan(&r.Outcome, &r.Step, &r.OldKey, &r.NewKey, &r.Error); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *env) source(id int64) sqlcgen.Source {
	e.t.Helper()
	s, err := e.store.Queries().GetSource(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "f95", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// f95Game inserts a tracked F95 Game for thread 67494 as an earlier fetch left it.
func (e *env) f95Game() (sqlcgen.Game, sqlcgen.Source) {
	g, s := testutil.InsertGame(e.t, e.store, testutil.GameSpec{
		Name: "Old name", ExternalID: "67494", LatestVersion: "v4.0", DevStatus: domain.DevCompleted,
	})
	e.exec(`UPDATE source SET details_pending = 1 WHERE id = ?`, s.ID)
	e.enqueue(s.ID)
	return g, s
}

func TestRefreshF95(t *testing.T) {
	e := newEnv(t, true)
	e.fake.SetThread("67494", fixture(t, "67494.html"), fixture(t, "59416.guest.html"))
	g, src := e.f95Game()
	// Earlier tags: a verified one, a wrong one, one the fetch will no longer find.
	err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		return e.tagsvc.ApplyAddTx(ctx, q, g.ID, tags.ParseCheckModel{
			Exact: []tags.Entry{
				{Raw: "Harem", Target: tags.TagRef{Kind: "f95", Slug: "harem", Label: "harem"}, Qualifier: "present", Accept: true},
				{Raw: "Vore", Target: tags.TagRef{Kind: "f95", Slug: "vore", Label: "vore"}, Qualifier: "present", Accept: true},
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := e.tagsvc.GameTags(ctx, g.ID)
	for _, r := range rows {
		v := tags.Confirmed
		if r.TagSlug == "vore" {
			v = tags.Wrong
		}
		if err := e.tagsvc.SetVerification(ctx, r.ID, v); err != nil {
			t.Fatal(err)
		}
	}

	runID := e.run()
	res, err := e.ref.Refresh(ctx, src.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFetched || res.OldDevStatus != "completed" || res.NewDevStatus != "ongoing" || res.Update() {
		t.Errorf("result = %+v", res)
	}
	s := e.source(src.ID)
	if s.DevStatus.String != "ongoing" {
		t.Errorf("dev status = %q, F95 must overwrite", s.DevStatus.String)
	}
	if s.LatestVersion.String != "v4.0" || s.ChangeKey.String != "v4.0" {
		t.Errorf("detail fetch must not touch the checker's version: %+v", s)
	}
	if s.DetailsPending != 0 || !s.LastDetailAt.Valid || !s.ThreadUpdatedAt.Valid {
		t.Errorf("source = %+v", s)
	}
	if !strings.Contains(s.GenreText.String, "Harem") {
		t.Errorf("genre text not stored: %q", s.GenreText.String)
	}
	if e.queued(src.ID) {
		t.Error("queue row not removed")
	}
	rs := e.results(src.ID)
	if len(rs) != 1 || rs[0].Step != "detail" || rs[0].Outcome != "fetched" {
		t.Errorf("results = %+v", rs)
	}
	game, _ := e.store.Queries().GetGame(ctx, g.ID)
	if game.Name != "Out of Touch!" {
		t.Errorf("name = %q", game.Name)
	}
	if !res.CoverFetched || !game.CoverPath.Valid {
		t.Errorf("cover: %+v %+v", res, game)
	}

	got := map[string]sqlcgen.ListGameTagsRow{}
	rows, _ = e.tagsvc.GameTags(ctx, g.ID)
	for _, r := range rows {
		got[r.TagSlug] = r
	}
	if got["harem"].Verification != tags.Confirmed || got["harem"].Origin != tags.OriginBoth {
		t.Errorf("harem = %+v", got["harem"])
	}
	if v := got["vore"]; v.Verification != tags.Wrong || !v.RemovedAtSourceAt.Valid {
		t.Errorf("vore is gone from the thread but keeps its verdict: %+v", v)
	}
	if r := got["voyeurism"]; r.IsNew != 1 || r.Verification != tags.Unverified {
		t.Errorf("new tag = %+v", r)
	}
	if res.Tags.New == 0 || res.Tags.Removed != 1 {
		t.Errorf("merge = %+v", res.Tags)
	}
}

func TestRefreshF95CoverFailureKeepsOld(t *testing.T) {
	e := newEnv(t, true)
	e.fake.SetThread("67494", fixture(t, "67494.html"), fixture(t, "59416.guest.html"))
	g, src := e.f95Game()
	run := e.run()
	if _, err := e.ref.Refresh(ctx, src.ID, run); err != nil {
		t.Fatal(err)
	}
	before, _ := e.store.Queries().GetGame(ctx, g.ID)
	e.exec(`UPDATE game SET cover_source_url = 'https://attachments.f95zone.to/old.png' WHERE id = ?`, g.ID)
	e.cover.status = 500
	res, err := e.ref.Refresh(ctx, src.ID, run)
	if err != nil {
		t.Fatalf("cover failure must not fail the refresh: %v", err)
	}
	after, _ := e.store.Queries().GetGame(ctx, g.ID)
	if res.CoverErr == nil || res.CoverFetched || after.CoverPath != before.CoverPath {
		t.Errorf("res=%+v before=%+v after=%+v", res, before, after)
	}
}

func TestRefreshF95ParseFailureLeavesDataUnchanged(t *testing.T) {
	e := newEnv(t, true)
	broken := strings.ReplaceAll(string(fixture(t, "67494.html")), "js-tagList", "js-gone")
	e.fake.SetThread("67494", []byte(broken), fixture(t, "59416.guest.html"))
	g, src := e.f95Game()
	before := e.source(src.ID)
	run := e.run()
	_, err := e.ref.Refresh(ctx, src.ID, run)
	if !errors.Is(err, f95.ErrParse) {
		t.Fatalf("err = %v", err)
	}
	if after := e.source(src.ID); after != before {
		t.Errorf("source changed: %+v → %+v", before, after)
	}
	if game, _ := e.store.Queries().GetGame(ctx, g.ID); game.Name != "Old name" {
		t.Errorf("name = %q", game.Name)
	}
	if rows, _ := e.tagsvc.GameTags(ctx, g.ID); len(rows) != 0 {
		t.Errorf("tags written: %v", rows)
	}
	rs := e.results(src.ID)
	if len(rs) != 1 || rs[0].Outcome != OutcomeError || !rs[0].Error.Valid || rs[0].Step != StepDetail {
		t.Errorf("results = %+v", rs)
	}
	if !e.queued(src.ID) {
		t.Error("failed fetch must stay queued")
	}
}

func TestRefreshF95FetchErrors(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(e *env)
		cookie bool
		want   error
	}{
		{"cookie invalid", func(e *env) { e.fake.SetValidUser("someone-else") }, true, f95.ErrCookieInvalid},
		{"restricted", func(e *env) { e.fake.Program("67494", testutil.F95Restricted()) }, true, f95.ErrRestricted},
		{"blocked", func(e *env) { e.fake.Program("67494", testutil.F95Challenge()) }, true, f95.ErrBlocked},
		{"guest answer without a cookie", func(e *env) {}, false, f95.ErrCookieInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.cookie)
			e.fake.SetThread("67494", fixture(t, "67494.html"), fixture(t, "59416.guest.html"))
			c.setup(e)
			g, src := e.f95Game()
			e.exec(`UPDATE source SET genre_text = 'Earlier text' WHERE id = ?`, src.ID)
			_, err := e.ref.Refresh(ctx, src.ID, e.run())
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			rs := e.results(src.ID)
			if len(rs) != 1 || !(rs[0].Outcome == OutcomeError || rs[0].Outcome == OutcomeSkipped) {
				t.Errorf("results = %+v", rs)
			}
			if !e.queued(src.ID) || e.source(src.ID).DetailsPending != 1 {
				t.Error("source and queue must be untouched")
			}
			if got := e.source(src.ID).GenreText.String; got != "Earlier text" {
				t.Errorf("genre text = %q, a failed or guest fetch must keep it", got)
			}
			if rows, _ := e.tagsvc.GameTags(ctx, g.ID); len(rows) != 0 {
				t.Errorf("tags written: %v", rows)
			}
		})
	}
}

func (e *env) itchGame(url, key string) sqlcgen.Source {
	_, s := testutil.InsertGame(e.t, e.store, testutil.GameSpec{Kind: domain.SourceItchio, URL: url, ExternalID: "x", ChangeKey: key, LatestVersion: "old"})
	return s
}

func TestRefreshItch(t *testing.T) {
	e := newEnv(t, false)
	body, err := os.ReadFile("../itch/testdata/s1.html")
	if err != nil {
		t.Fatal(err)
	}
	e.ifake.Set("/lycoris-radiata", body)
	src := e.itchGame("https://kuro.itch.io/lycoris-radiata", "updated=old")
	run := e.run()

	res, err := e.ref.Refresh(ctx, src.ID, run)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Update() || res.OldKey != "updated=old" || !strings.HasPrefix(res.NewKey, "updated=2026-09-20T10:49:00Z|") {
		t.Errorf("result = %+v", res)
	}
	s := e.source(src.ID)
	if s.ChangeKey.String != res.NewKey || s.LatestVersion.String != "Ch7" || s.ThreadUpdatedAt.String != "2026-09-20T10:49:00Z" || !s.LastCheckedAt.Valid {
		t.Errorf("source = %+v", s)
	}
	// Same page again: unchanged.
	res, err = e.ref.Refresh(ctx, src.ID, run)
	if err != nil || res.Outcome != OutcomeUnchanged {
		t.Fatalf("second = %+v %v", res, err)
	}
	rs := e.results(src.ID)
	if len(rs) != 2 || rs[0].Outcome != OutcomeUpdate || rs[0].OldKey.String != "updated=old" || rs[0].NewKey.String != s.ChangeKey.String || rs[0].Step != StepItchPage || rs[1].Outcome != OutcomeUnchanged {
		t.Errorf("results = %+v", rs)
	}
}

func TestRefreshItchFirstAnswerIsNotAnUpdate(t *testing.T) {
	e := newEnv(t, false)
	body, _ := os.ReadFile("../itch/testdata/s1.html")
	e.ifake.Set("/g", body)
	_, s := testutil.InsertGame(t, e.store, testutil.GameSpec{Kind: domain.SourceItchio, URL: "https://a.itch.io/g", ExternalID: "g"})
	res, err := e.ref.Refresh(ctx, s.ID, e.run())
	if err != nil || res.Outcome != OutcomeFetched {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRefreshItchNotTrackable(t *testing.T) {
	e := newEnv(t, false)
	body, _ := os.ReadFile("../itch/testdata/s3.html")
	e.ifake.Set("/alchemy-shop", body)
	src := e.itchGame("https://jjambong.itch.io/alchemy-shop", "k")
	res, err := e.ref.Refresh(ctx, src.ID, e.run())
	if err != nil || !res.NotTrackable || res.Outcome != OutcomeSkipped {
		t.Fatalf("%+v %v", res, err)
	}
	s := e.source(src.ID)
	if s.ChecksEnabled != 0 || s.ChangeKey.String != "k" {
		t.Errorf("source = %+v", s)
	}
}

func TestRefreshItchMissAndBlocked(t *testing.T) {
	e := newEnv(t, false)
	src := e.itchGame("https://a.itch.io/gone", "k")
	_, err := e.ref.Refresh(ctx, src.ID, e.run())
	if !errors.Is(err, itch.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	rs := e.results(src.ID)
	if len(rs) != 1 || rs[0].Outcome != OutcomeMiss || e.source(src.ID).MissCount != 0 {
		t.Errorf("results = %+v", rs)
	}
}

func TestRefreshRejectsNonPrimaryAndManual(t *testing.T) {
	e := newEnv(t, false)
	_, s := testutil.InsertGame(t, e.store, testutil.GameSpec{Kind: domain.SourceManual})
	if _, err := e.ref.Refresh(ctx, s.ID, e.run()); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("manual err = %v", err)
	}
	if _, err := e.ref.Refresh(ctx, 9999, e.run()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
}

func TestManualRun(t *testing.T) {
	e := newEnv(t, false)
	id, finish, err := e.ref.StartManualRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := e.store.Queries().GetLatestCheckRun(ctx)
	if run.ID != id || run.Kind != "manual" || run.Status != "running" || run.FinishedAt.Valid {
		t.Fatalf("run = %+v", run)
	}
	e.clk.Advance(time.Minute)
	finish("partial")
	run, _ = e.store.Queries().GetLatestCheckRun(ctx)
	if run.Status != "partial" || run.FinishedAt.String != "2026-10-05T08:01:00Z" {
		t.Errorf("finished run = %+v", run)
	}
}
