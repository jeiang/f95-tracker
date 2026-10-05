package web

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type detailEnv struct {
	t     *testing.T
	store *db.Store
	s     *Server
	h     http.Handler
	hdr   map[string]string
	fake  *testutil.F95Fake
	creds *f95.CredStore
	state string
	raw   *sql.DB
}

func newDetailEnv(t *testing.T) *detailEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(dir, "state", "f95-tracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	dbPath := filepath.Join(dir, "state", "f95-tracker.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	oidc := testutil.NewOIDCFake(t)
	cfg := oidc.Apply(newTestConfig())
	clk := clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := filepath.Join(dir, "state")
	fake := testutil.NewF95Fake(t)
	creds := f95.NewCredStore(store, clk)
	fc, err := f95.New(f95.Options{BaseURL: fake.URL(), Creds: creds, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	g := games.New(store, clk, games.Options{StateDir: state, HTTPClient: &http.Client{Transport: pngTransport{}}})
	tg := tags.New(store, clk)
	ic := itch.NewClient(itch.Options{Clock: clk, BaseURL: testutil.NewItchFake(t).URL})
	s := New(Deps{Store: store, Clock: clk, Log: log, Config: cfg, Auth: auth.New(cfg, store, clk, log),
		Games: g, Tags: tg, F95: fc, Refresher: check.NewRefresher(store, clk, g, tg, fc, ic, log)})
	h, c := signedIn(t, s, oidc)
	return &detailEnv{t: t, store: store, s: s, h: h, hdr: map[string]string{"Cookie": c.Name + "=" + c.Value}, fake: fake, creds: creds, state: state, raw: raw}
}

type pngTransport struct{}

func (pngTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}},
		Body: io.NopCloser(strings.NewReader("\x89PNG fake")), Request: req}, nil
}

func (e *detailEnv) post(path string, htmx bool, kv ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range e.hdr {
		req.Header.Set(k, v)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *detailEnv) get(path string) *httptest.ResponseRecorder { return do(e.h, "GET", path, e.hdr) }

func (e *detailEnv) game(name string, kind domain.SourceKind, ext, link string) games.Created {
	e.t.Helper()
	c, err := e.s.games.Create(context.Background(), games.CreateParams{
		Name: name, Source: games.SourceSpec{Kind: kind, ExternalID: ext, URL: link}, LatestVersion: "v1.0",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *detailEnv) manual(name string) games.Created {
	return e.game(name, domain.SourceManual, "", "https://example.com/"+strings.ReplaceAll(name, " ", "-"))
}

func (e *detailEnv) f95game(name, thread string) games.Created {
	return e.game(name, domain.SourceF95Thread, thread, "https://f95zone.to/threads/"+thread+"/")
}

func (e *detailEnv) detail(id int64) games.GameDetail {
	e.t.Helper()
	d, err := e.s.games.Detail(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func path(id int64, rest string) string { return "/games/" + itoa64(id) + rest }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func isFragment(rec *httptest.ResponseRecorder, id string) bool {
	b := rec.Body.String()
	return rec.Code == 200 && strings.Contains(b, `id="`+id+`"`) && !strings.Contains(b, "<html")
}

func TestGameDetailUnknownIs404(t *testing.T) {
	e := newDetailEnv(t)
	for _, c := range []struct{ m, p string }{
		{"GET", "/games/99"}, {"GET", "/games/abc"}, {"POST", "/games/99/play-status"}, {"POST", "/games/99/rating"},
		{"POST", "/games/99/play-log"}, {"POST", "/games/99/tags"}, {"POST", "/games/99/delete"}, {"POST", "/games/99/refresh"},
	} {
		var rec *httptest.ResponseRecorder
		if c.m == "GET" {
			rec = e.get(c.p)
		} else {
			rec = e.post(c.p, false, "play_status", "playing", "rating_x2", "3", "version", "v1", "tag", "x", "confirm", "yes")
		}
		if rec.Code != 404 {
			t.Errorf("%s %s: %d", c.m, c.p, rec.Code)
		}
	}
}

func TestGameDetailPageContents(t *testing.T) {
	e := newDetailEnv(t)
	c := e.f95game("Eternum", "67494")
	if _, err := e.s.tags.AddByHand(context.Background(), c.Game.ID, tags.TagRef{Kind: tags.KindCustom, Label: "Cozy"}, ""); err != nil {
		t.Fatal(err)
	}
	rec := e.get(path(c.Game.ID, ""))
	body := rec.Body.String()
	for _, want := range []string{"Eternum", "F95 thread #67494", `id="play-log"`, `id="tag-list"`, `id="error-slot"`, "Cozy", "Present", "by hand", "Refresh from Source"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="dev_status"`) {
		t.Error("F95 Dev status must be read-only")
	}
	manual := e.manual("Manual one")
	if body := e.get(path(manual.Game.ID, "")).Body.String(); !strings.Contains(body, `name="dev_status"`) || strings.Contains(body, "Refresh from Source") {
		t.Error("manual Source: Dev status select, no refresh")
	}
}

func TestPlayStatusAndRating(t *testing.T) {
	e := newDetailEnv(t)
	id := e.manual("G").Game.ID
	rec := e.post(path(id, "/play-status"), true, "play_status", "playing")
	if !isFragment(rec, "game-detail") || e.detail(id).Game.PlayStatus != "playing" {
		t.Fatalf("htmx play-status: %d", rec.Code)
	}
	rec = e.post(path(id, "/play-status"), false, "play_status", "finished")
	if rec.Code != 303 || rec.Header().Get("Location") != path(id, "") || e.detail(id).Game.PlayStatus != "finished" {
		t.Fatalf("plain play-status: %d %v", rec.Code, rec.Header())
	}
	if rec = e.post(path(id, "/play-status"), true, "play_status", "bogus"); rec.Code != 422 || rec.Header().Get("HX-Retarget") != "#error-slot" {
		t.Fatalf("bad status: %d", rec.Code)
	}

	for _, c := range []struct {
		x2   string
		code int
		want int64
		set  bool
	}{
		{"1", 303, 1, true}, {"7", 303, 7, true}, {"10", 303, 10, true}, {"0", 303, 0, false},
		{"11", 422, 0, false}, {"-1", 422, 0, false}, {"x", 422, 0, false},
	} {
		e.post(path(id, "/rating"), false, "rating_x2", "7")
		rec := e.post(path(id, "/rating"), false, "rating_x2", c.x2)
		r := e.detail(id).Game.RatingX2
		if rec.Code != c.code {
			t.Errorf("rating %s: %d", c.x2, rec.Code)
		}
		switch {
		case c.code == 422 && r.Int64 != 7:
			t.Errorf("rating %s changed stored value to %v", c.x2, r)
		case c.code == 303 && (r.Valid != c.set || r.Int64 != c.want):
			t.Errorf("rating %s stored %v", c.x2, r)
		}
	}
	if rec := e.post(path(id, "/rating"), true, "rating_x2", "4"); !isFragment(rec, "game-detail") {
		t.Errorf("htmx rating: %d", rec.Code)
	}
}

func TestPlayLogReviewRedirectAndLastPlayed(t *testing.T) {
	e := newDetailEnv(t)
	ctx := context.Background()
	withTags := e.manual("Tagged").Game.ID
	if _, err := e.s.tags.AddByHand(ctx, withTags, tags.TagRef{Kind: tags.KindCustom, Label: "Cozy"}, "present"); err != nil {
		t.Fatal(err)
	}
	// First user entry with present tags: redirect to the review (htmx and plain).
	rec := e.post(path(withTags, "/play-log"), true, "version", "v1.0", "played_on", "2026-01-02")
	if rec.Code != 200 || rec.Header().Get("HX-Redirect") != path(withTags, "/review?version=v1.0") {
		t.Fatalf("htmx first entry: %d %v", rec.Code, rec.Header())
	}
	rec = e.post(path(withTags, "/play-log"), false, "version", "v1.1", "played_on", "2026-01-03")
	if rec.Code != 303 || rec.Header().Get("Location") != path(withTags, "") { // hand-confirmed tags leave nothing to review
		t.Fatalf("second entry: %d %v", rec.Code, rec.Header())
	}

	// No tags: back to the Game (fragment / redirect), never the review.
	id := e.manual("Plain").Game.ID
	rec = e.post(path(id, "/play-log"), true, "version", "v1.0", "played_on", "2026-02-01")
	if !isFragment(rec, "game-detail") || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("no-tag htmx: %d %v", rec.Code, rec.Header())
	}
	rec = e.post(path(id, "/play-log"), false, "version", "v0.9", "played_on", "2025-12-01") // back-dated
	if rec.Code != 303 || rec.Header().Get("Location") != path(id, "") {
		t.Fatalf("no-tag plain: %d %v", rec.Code, rec.Header())
	}
	if lp := e.detail(id).LastPlayed; lp == nil || lp.Version != "v1.0" {
		t.Fatalf("back-dated entry must not become last played: %+v", lp)
	}
	if rec = e.post(path(id, "/play-log"), true, "version", "v2", "played_on", "02/03/2026"); rec.Code != 422 {
		t.Fatalf("bad date: %d", rec.Code)
	}

	// Edit and delete recompute last played.
	var entries []int64
	for _, pl := range e.detail(id).PlayLog {
		entries = append(entries, pl.ID)
	}
	latest := e.detail(id).LastPlayed.PlayLogID
	rec = e.post(path(id, "/play-log/"+itoa64(latest)), true, "version", "v1.0", "played_on", "2025-11-01")
	if !isFragment(rec, "game-detail") {
		t.Fatalf("edit: %d", rec.Code)
	}
	if lp := e.detail(id).LastPlayed; lp.Version != "v0.9" {
		t.Fatalf("after edit last played = %+v", lp)
	}
	other := withTags
	if rec = e.post(path(id, "/play-log/"+itoa64(entries[0]+1000)), false, "version", "v", "played_on", "2026-01-01"); rec.Code != 404 {
		t.Fatalf("unknown entry: %d", rec.Code)
	}
	if rec = e.post(path(other, "/play-log/"+itoa64(latest)+"/delete"), false); rec.Code != 404 {
		t.Fatalf("entry of another Game: %d", rec.Code)
	}
	for _, pl := range e.detail(id).PlayLog {
		if rec = e.post(path(id, "/play-log/"+itoa64(pl.ID)+"/delete"), false); rec.Code != 303 {
			t.Fatalf("delete: %d", rec.Code)
		}
	}
	if d := e.detail(id); d.LastPlayed != nil || len(d.PlayLog) != 0 {
		t.Fatalf("after deleting all: %+v", d.LastPlayed)
	}
}

func TestImportedEntryShownUndated(t *testing.T) {
	e := newDetailEnv(t)
	id := e.manual("Imp").Game.ID
	if _, err := e.raw.Exec(`INSERT INTO play_log(game_id, version, played_on, origin, created_at) VALUES (?, ?, NULL, ?, ?)`, id, "v0.5", "imported", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	body := e.get(path(id, "")).Body.String()
	if !strings.Contains(body, "imported") || !strings.Contains(body, "undated") {
		t.Error("imported entry must show imported and undated")
	}
}

func TestDevStatusEdit(t *testing.T) {
	e := newDetailEnv(t)
	f := e.f95game("F", "111")
	rec := e.post(path(f.Game.ID, "/sources/"+itoa64(f.Source.ID)), true, "action", "dev_status", "dev_status", "completed")
	if rec.Code != 422 || e.detail(f.Game.ID).Primary.DevStatus.Valid {
		t.Fatalf("F95 Dev status edit: %d", rec.Code)
	}
	m := e.manual("M")
	rec = e.post(path(m.Game.ID, "/sources/"+itoa64(m.Source.ID)), true, "action", "dev_status", "dev_status", "abandoned")
	if !isFragment(rec, "game-detail") || e.detail(m.Game.ID).Primary.DevStatus.String != "abandoned" {
		t.Fatalf("manual Dev status: %d", rec.Code)
	}
	e.post(path(m.Game.ID, "/sources/"+itoa64(m.Source.ID)), false, "action", "dev_status", "dev_status", "")
	if e.detail(m.Game.ID).Primary.DevStatus.Valid {
		t.Error("empty selection clears the Dev status")
	}
	// A Source of another Game is not addressable.
	if rec = e.post(path(m.Game.ID, "/sources/"+itoa64(f.Source.ID)), false, "action", "reenable"); rec.Code != 404 {
		t.Fatalf("foreign source: %d", rec.Code)
	}
}

func TestSourcesAddPrimaryReenable(t *testing.T) {
	e := newDetailEnv(t)
	c := e.manual("S")
	id := c.Game.ID
	if rec := e.post(path(id, "/sources"), true, "url", "https://someone.itch.io/cool-game?x=1"); !isFragment(rec, "game-detail") {
		t.Fatalf("add itch link: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.post(path(id, "/sources"), false, "url", "https://someone.itch.io/cool-game"); rec.Code != 409 {
		t.Fatalf("duplicate link: %d", rec.Code)
	}
	if rec := e.post(path(id, "/sources"), false, "url", "ftp://nope"); rec.Code != 422 {
		t.Fatalf("bad link: %d", rec.Code)
	}
	d := e.detail(id)
	if len(d.Sources) != 2 {
		t.Fatalf("sources = %d", len(d.Sources))
	}
	var link int64
	for _, s := range d.Sources {
		if s.IsPrimary == 0 {
			link = s.ID
		}
	}
	if rec := e.post(path(id, "/sources/"+itoa64(link)), false, "action", "primary"); rec.Code != 303 || e.detail(id).Primary.ID != link {
		t.Fatalf("set primary: %d", rec.Code)
	}
	if _, err := e.raw.Exec(`UPDATE source SET unavailable_at='2026-10-01T00:00:00Z', miss_count=3, checks_enabled=0 WHERE id=?`, link); err != nil {
		t.Fatal(err)
	}
	if body := e.get(path(id, "")).Body.String(); !strings.Contains(body, "Re-enable checks") || !strings.Contains(body, "Source unavailable") {
		t.Error("unavailable Source shows Re-enable checks")
	}
	e.post(path(id, "/sources/"+itoa64(link)), true, "action", "reenable")
	if p := e.detail(id).Primary; p.UnavailableAt.Valid || p.ChecksEnabled != 1 || p.MissCount != 0 {
		t.Fatalf("after re-enable: %+v", p)
	}
}

func TestTagsByHandAndEdit(t *testing.T) {
	e := newDetailEnv(t)
	ctx := context.Background()
	id := e.manual("T").Game.ID
	rec := e.post(path(id, "/tags"), true, "tag", "Wholesome Vibes")
	if !isFragment(rec, "tag-list") || !strings.Contains(rec.Body.String(), "Wholesome Vibes") {
		t.Fatalf("add tag: %d", rec.Code)
	}
	rows, _ := e.s.tags.GameTags(ctx, id)
	if len(rows) != 1 || rows[0].Verification != tags.Confirmed || rows[0].Origin != tags.OriginManual || rows[0].TagKind != tags.KindCustom {
		t.Fatalf("hand tag: %+v", rows)
	}
	tid := rows[0].ID
	for _, c := range []struct{ kv []string }{{[]string{"verification", "wrong"}}, {[]string{"qualifier", "optional"}}} {
		if rec := e.post(path(id, "/tags/"+itoa64(tid)), true, c.kv...); !isFragment(rec, "tag-list") {
			t.Fatalf("%v: %d", c.kv, rec.Code)
		}
	}
	rows, _ = e.s.tags.GameTags(ctx, id)
	if rows[0].Verification != tags.Wrong || rows[0].Qualifier != tags.QualOptional {
		t.Fatalf("after edits: %+v", rows[0])
	}
	if rec := e.post(path(id, "/tags/"+itoa64(tid)), false, "verification", "maybe"); rec.Code != 422 {
		t.Fatalf("bad verification: %d", rec.Code)
	}
	if rec := e.post(path(id, "/tags/"+itoa64(tid)), false); rec.Code != 422 {
		t.Fatalf("empty edit: %d", rec.Code)
	}
	other := e.manual("Other").Game.ID
	if rec := e.post(path(other, "/tags/"+itoa64(tid)), false, "verification", "confirmed"); rec.Code != 404 {
		t.Fatalf("tag of another Game: %d", rec.Code)
	}
	if rec := e.post(path(id, "/tags"), false, "tag", "  "); rec.Code != 422 {
		t.Fatalf("empty tag: %d", rec.Code)
	}
}

func TestRefresh(t *testing.T) {
	e := newDetailEnv(t)
	ctx := context.Background()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "f95", "67494.html"))
	if err != nil {
		t.Fatal(err)
	}
	guest, err := os.ReadFile(filepath.Join("..", "..", "testdata", "f95", "59416.guest.html"))
	if err != nil {
		t.Fatal(err)
	}
	e.fake.SetThread("67494", body, guest)
	c := e.f95game("Old name", "67494")

	// No cookie stored: the guest page is not applied; inline message, state kept.
	rec := e.post(path(c.Game.ID, "/refresh"), true)
	if rec.Code != http.StatusBadGateway || !isFragment(&httptest.ResponseRecorder{Code: 200, Body: rec.Body}, "game-detail") ||
		!strings.Contains(rec.Body.String(), "cookie is no longer valid") {
		t.Fatalf("cookie invalid: %d %s", rec.Code, rec.Body.String())
	}
	if e.detail(c.Game.ID).Game.Name != "Old name" {
		t.Fatal("failed refresh changed the Game")
	}
	if rec = e.post(path(c.Game.ID, "/refresh"), false); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("plain failure renders the page: %d", rec.Code)
	}

	e.fake.Program("67494", testutil.F95Restricted())
	if rec = e.post(path(c.Game.ID, "/refresh"), true); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "restricted") {
		t.Fatalf("restricted: %d", rec.Code)
	}

	if err := e.creds.Replace(ctx, f95.Jar{"xf_user": "user1", "xf_tfa_trust": "t"}, "TestBrowser/1.0", time.Time{}); err != nil {
		t.Fatal(err)
	}
	rec = e.post(path(c.Game.ID, "/refresh"), true)
	if !isFragment(rec, "game-detail") || !strings.Contains(rec.Body.String(), "Checked just now") {
		t.Fatalf("success: %d %s", rec.Code, rec.Body.String())
	}
	d := e.detail(c.Game.ID)
	if d.Game.Name == "Old name" || !d.Game.CoverPath.Valid {
		t.Fatalf("refresh did not apply: %+v", d.Game)
	}
	if rows, _ := e.s.tags.GameTags(ctx, c.Game.ID); len(rows) == 0 {
		t.Fatal("refresh added no tags")
	}

	if rec = e.post(path(e.manual("M").Game.ID, "/refresh"), true); rec.Code != 422 {
		t.Fatalf("manual Source refresh: %d", rec.Code)
	}
}

func TestRefreshFailureMessages(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		text   string
	}{
		{f95.ErrBusy, 503, "F95 busy, try again"},
		{f95.ErrCookieInvalid, 502, "cookie"},
		{f95.ErrBlocked, 502, "rate limiting"},
		{f95.ErrRestricted, 502, "restricted"},
		{f95.ErrParse, 502, "unexpected"},
		{errors.New("boom"), 0, ""},
	} {
		msg, status := refreshFailure(c.err)
		if status != c.status || !strings.Contains(msg, c.text) {
			t.Errorf("%v: %d %q", c.err, status, msg)
		}
	}
}

func TestDeleteGameNeedsConfirm(t *testing.T) {
	e := newDetailEnv(t)
	id := e.manual("Bye").Game.ID
	if rec := e.post(path(id, "/delete"), false); rec.Code != 422 {
		t.Fatalf("unconfirmed: %d", rec.Code)
	}
	if rec := e.post(path(id, "/delete"), true, "confirm", "yes"); rec.Code != 200 || rec.Header().Get("HX-Redirect") != "/games" {
		t.Fatalf("htmx delete: %d %v", rec.Code, rec.Header())
	}
	if rec := e.get(path(id, "")); rec.Code != 404 {
		t.Fatalf("after delete: %d", rec.Code)
	}
	id = e.manual("Bye2").Game.ID
	if rec := e.post(path(id, "/delete"), false, "confirm", "yes"); rec.Code != 303 || rec.Header().Get("Location") != "/games" {
		t.Fatalf("plain delete: %d", rec.Code)
	}
}

func TestCover(t *testing.T) {
	e := newDetailEnv(t)
	ctx := context.Background()
	id := e.manual("C").Game.ID
	if rec := e.get("/covers/" + itoa64(id)); rec.Code != 404 {
		t.Fatalf("no cover: %d", rec.Code)
	}
	if rec := e.get("/covers/999"); rec.Code != 404 {
		t.Fatalf("unknown game: %d", rec.Code)
	}
	if err := e.s.games.FetchCover(ctx, id, "https://img.example/c.png"); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/covers/" + itoa64(id))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") == "" || rec.Body.String() != "\x89PNG fake" {
		t.Fatalf("cover: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(e.get(path(id, "")).Body.String(), "/covers/"+itoa64(id)) {
		t.Error("page does not reference the cover")
	}
	if rec := do(e.h, "GET", "/covers/"+itoa64(id), nil); rec.Code == 200 {
		t.Error("cover requires a session")
	}
}

func TestLinkSourceSpec(t *testing.T) {
	for _, c := range []struct {
		in   string
		kind domain.SourceKind
		ext  string
		url  string
		bad  bool
	}{
		{"https://f95zone.to/threads/eternum.67494/post-1", domain.SourceF95Thread, "67494", "https://f95zone.to/threads/67494/", false},
		{"https://Someone.itch.io/Cool-Game/?a=1", domain.SourceItchio, "someone.itch.io/cool-game", "https://someone.itch.io/Cool-Game", false},
		{"https://example.com/g", domain.SourceManual, "", "https://example.com/g", false},
		{"nope", "", "", "", true},
	} {
		sp, err := linkSourceSpec(c.in)
		if c.bad != (err != nil) || (!c.bad && (sp.Kind != c.kind || sp.ExternalID != c.ext || sp.URL != c.url)) {
			t.Errorf("%q: %+v %v", c.in, sp, err)
		}
	}
}
