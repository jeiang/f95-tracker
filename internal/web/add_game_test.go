package web

import (
	"context"
	"database/sql"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/genre"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type addEnv struct {
	t      *testing.T
	store  *db.Store
	raw    *sql.DB
	h      http.Handler
	cookie string
	f95    *testutil.F95Fake
	itch   *testutil.ItchFake
	tags   *tags.Service
	lock   string
}

type noNetwork struct{}

func (noNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 404, Body: http.NoBody, Header: http.Header{}}, nil
}

func newAddEnv(t *testing.T, cookie bool, busyWait time.Duration) *addEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "f95-tracker.db")
	store, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })

	oidc := testutil.NewOIDCFake(t)
	cfg := oidc.Apply(newTestConfig())
	clk := clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &addEnv{t: t, store: store, raw: raw, f95: testutil.NewF95Fake(t), itch: testutil.NewItchFake(t), lock: filepath.Join(dir, "f95.lock")}

	creds := f95.NewCredStore(store, clk)
	if cookie {
		if err := creds.Replace(context.Background(), f95.Jar{"xf_user": "user1"}, "TestBrowser/1.0", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	opt := f95.Options{BaseURL: e.f95.URL(), Creds: creds, Version: "test", Interactive: true}
	if busyWait > 0 {
		opt.Pacer = &f95.Pacer{LockPath: e.lock, MaxWait: busyWait}
	}
	fc, err := f95.New(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	ic := itch.NewClient(itch.Options{Clock: clk, BaseURL: e.itch.URL})
	g := games.New(store, clk, games.Options{StateDir: filepath.Join(dir, "state"), HTTPClient: &http.Client{Transport: noNetwork{}}})
	e.tags = tags.New(store, clk)
	s := New(Deps{
		Store: store, Clock: clk, Log: log, Config: cfg, Auth: auth.New(cfg, store, clk, log),
		Games: g, Tags: e.tags, Itch: ic, F95: fc, Refresher: check.NewRefresher(store, clk, g, e.tags, fc, ic, log),
	})
	h, c := signedIn(t, s, oidc)
	e.h, e.cookie = h, c.Name+"="+c.Value
	return e
}

func (e *addEnv) post(path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", e.cookie)
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *addEnv) fetch(kind, input string, extra url.Values) *httptest.ResponseRecorder {
	f := url.Values{"kind": {kind}, "input": {input}}
	for k, v := range extra {
		f[k] = v
	}
	return e.post("/games/fetch", f)
}

func (e *addEnv) loadThread(id, loggedIn, guest string) {
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join("../../testdata/f95", n))
		if err != nil {
			e.t.Fatal(err)
		}
		return b
	}
	e.f95.SetThread(id, read(loggedIn), read(guest))
}

var hiddenRe = regexp.MustCompile(`<input type="hidden" name="([^"]*)" value="([^"]*)"/?>`)

// confirmForm turns a fetch response into the form Confirm would post.
func confirmForm(t *testing.T, body string) url.Values {
	t.Helper()
	f := url.Values{}
	ms := hiddenRe.FindAllStringSubmatch(body, -1)
	if len(ms) == 0 {
		t.Fatalf("no hidden fields in:\n%s", body)
	}
	for _, m := range ms {
		f.Set(m[1], html.UnescapeString(m[2]))
	}
	if m := nameRe.FindStringSubmatch(body); m != nil {
		f.Set("name", html.UnescapeString(m[1]))
	}
	return f
}

var nameRe = regexp.MustCompile(`<input id="add-name" name="name"[^>]*value="([^"]*)"`)

// acceptAll ticks every checkbox the parse panels offered.
func acceptAll(f url.Values) {
	for k := range f {
		if strings.HasSuffix(k, ".raw") {
			f.Set(strings.TrimSuffix(k, ".raw")+".accept", "1")
		}
		if strings.HasSuffix(k, ".slug") && strings.HasPrefix(k, "fo.") {
			f.Set(strings.TrimSuffix(k, ".slug")+".accept", "1")
		}
	}
}

func (e *addEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.raw.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func countMatches(body, re string) int { return len(regexp.MustCompile(re).FindAllString(body, -1)) }

func TestAddFetchRendersParsePanels(t *testing.T) {
	e := newAddEnv(t, true, 0)
	e.loadThread("67494", "67494.html", "67494.html")
	rec := e.fetch("f95_thread", "https://f95zone.to/threads/out-of-touch.67494/", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()

	th, err := e.f95Thread("67494")
	if err != nil {
		t.Fatal(err)
	}
	want, err := e.tags.ParseCheck(context.Background(), th.GenreText, th.Tags)
	if err != nil {
		t.Fatal(err)
	}
	if len(want.NeedsLook) == 0 || len(want.Exact) == 0 || len(want.F95Only) == 0 {
		t.Fatalf("fixture should fill all three panels: %d/%d/%d", len(want.NeedsLook), len(want.Exact), len(want.F95Only))
	}
	for name, c := range map[string][2]any{
		"nl": {`name="nl\.\d+\.raw"`, len(want.NeedsLook)},
		"ex": {`name="ex\.\d+\.raw"`, len(want.Exact)},
		"fo": {`name="fo\.\d+\.slug"`, len(want.F95Only)},
	} {
		if got := countMatches(body, c[0].(string)); got != c[1].(int) {
			t.Errorf("%s rows = %d, want %d", name, got, c[1])
		}
	}
	for _, s := range []string{"Out of Touch!", "v4.34.1 Public", "hl-exact", "unverified"} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM game`); n != 0 {
		t.Errorf("fetch added %d games", n)
	}
}

func (e *addEnv) f95Thread(id string) (*f95.Thread, error) {
	body, err := os.ReadFile(filepath.Join("../../testdata/f95", id+".html"))
	if err != nil {
		return nil, err
	}
	return f95.ParseThread(body, id, "https://f95zone.to/threads/"+id+"/", true)
}

func TestAddConfirmCreatesGameWithUnverifiedTags(t *testing.T) {
	e := newAddEnv(t, true, 0)
	e.loadThread("67494", "67494.html", "67494.html")
	form := confirmForm(t, e.fetch("f95_thread", "67494", nil).Body.String())

	th, _ := e.f95Thread("67494")
	model, _ := e.tags.ParseCheck(context.Background(), th.GenreText, th.Tags)
	// Re-point one no-match phrase at an F95 tag.
	remap := -1
	var raw string
	for i, en := range model.NeedsLook {
		if en.PhraseKey != "" && en.Reason == tags.ReasonNoMatch {
			remap, raw = i, en.Raw
			break
		}
	}
	if remap < 0 {
		t.Fatal("fixture has no no-match phrase to remap")
	}
	idx := strings.TrimPrefix(strings.TrimSuffix(findKey(t, form, raw, "nl."), ".raw"), "nl.")
	form.Set("nl."+idx+".target", "Remapped Custom")
	form.Set("play", "playing")
	acceptAll(form)

	rec := e.post("/games", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var gameID int64
	if err := e.raw.QueryRow(`SELECT id FROM game`).Scan(&gameID); err != nil {
		t.Fatal(err)
	}
	if loc := rec.Header().Get("Location"); loc != "/games/"+itoa(gameID) {
		t.Errorf("redirect %q", loc)
	}

	var name, play string
	var ver, key sql.NullString
	var pending int
	if err := e.raw.QueryRow(`SELECT g.name, g.play_status, s.latest_version, s.change_key, s.details_pending FROM game g JOIN source s ON s.game_id = g.id`).Scan(&name, &play, &ver, &key, &pending); err != nil {
		t.Fatal(err)
	}
	if name != "Out of Touch!" || play != "playing" || ver.String != "v4.34.1 Public" || key.String != "v4.34.1 Public" || pending != 0 {
		t.Errorf("game = %q %q %v %v pending=%d", name, play, ver, key, pending)
	}
	if n := e.count(`SELECT COUNT(*) FROM check_result WHERE outcome = 'update'`); n != 0 {
		t.Errorf("baseline raised %d Updates", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM check_run WHERE kind = 'manual'`); n != 1 {
		t.Errorf("manual runs = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM tag_review`); n != 0 {
		t.Errorf("tag_review rows = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM detail_fetch_queue`); n != 0 {
		t.Errorf("queued fetches = %d", n)
	}

	if n := e.count(`SELECT COUNT(*) FROM game_tag WHERE verification <> 'unverified'`); n != 0 {
		t.Errorf("%d verified tags after add", n)
	}
	if e.count(`SELECT COUNT(*) FROM game_tag WHERE f95_only = 1 AND origin = 'f95_list'`) != len(model.F95Only) {
		t.Errorf("f95_only rows != %d", len(model.F95Only))
	}
	if e.count(`SELECT COUNT(*) FROM game_tag gt JOIN tag t ON t.id = gt.tag_id WHERE t.slug = 'harem' AND gt.origin = 'both'`) != 1 {
		t.Error("Harem (in Genre text and F95 list) should have origin both")
	}
	if e.count(`SELECT COUNT(*) FROM game_tag WHERE origin = 'genre'`) == 0 {
		t.Error("expected genre-only tags (custom)")
	}
	// The remapped phrase is saved as a user Synonym and the tag carries the override.
	if e.count(`SELECT COUNT(*) FROM synonym WHERE phrase_key = ?`, genreKey(raw)) != 1 {
		t.Errorf("no Synonym saved for %q", raw)
	}
	if e.count(`SELECT COUNT(*) FROM game_tag WHERE mapping_override = 1 AND source_phrase = ?`, raw) != 1 {
		t.Errorf("mapping override missing for %q", raw)
	}
}

func TestAddIgnoredRowsAddNoTags(t *testing.T) {
	e := newAddEnv(t, true, 0)
	e.loadThread("67494", "67494.html", "67494.html")
	form := confirmForm(t, e.fetch("f95_thread", "67494", nil).Body.String())
	rec := e.post("/games", form) // no checkbox ticked: everything ignored
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if n := e.count(`SELECT COUNT(*) FROM game_tag`); n != 0 {
		t.Errorf("tags = %d", n)
	}
}

func TestAddAlreadyTrackedLinksToGame(t *testing.T) {
	e := newAddEnv(t, true, 0)
	e.loadThread("67494", "67494.html", "67494.html")
	form := confirmForm(t, e.fetch("f95_thread", "67494", nil).Body.String())
	if rec := e.post("/games", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("first add: %d", rec.Code)
	}
	var id int64
	e.raw.QueryRow(`SELECT id FROM game`).Scan(&id)

	rec := e.fetch("f95_thread", "https://f95zone.to/threads/x.67494/", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `href="/games/`+itoa(id)+`"`) {
		t.Fatalf("fetch of tracked thread: %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := e.post("/games", form); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `href="/games/`+itoa(id)+`"`) {
		t.Fatalf("second confirm: %d", rec.Code)
	}
	if n := e.count(`SELECT COUNT(*) FROM game`); n != 1 {
		t.Errorf("games = %d", n)
	}
}

func TestAddInvalidInput(t *testing.T) {
	e := newAddEnv(t, true, 0)
	for _, c := range []struct {
		kind, input string
		extra       url.Values
	}{
		{"f95_thread", "not a thread", nil},
		{"f95_thread", "https://example.com/threads/abc/", nil},
		{"itchio", "https://example.com/game", nil},
		{"itchio", "https://user.itch.io/", nil},
		{"manual", "ftp://x/y", url.Values{"name": {"X"}}},
		{"manual", "https://x.test/y", nil}, // name missing
	} {
		rec := e.fetch(c.kind, c.input, c.extra)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `role="alert"`) {
			t.Errorf("%s %q: status %d", c.kind, c.input, rec.Code)
		}
	}
	if len(e.f95.Requests()) != 0 || len(e.itch.Requests()) != 0 {
		t.Error("invalid input reached the network")
	}
}

func TestAddCookieInvalidAddsWithDetailsPending(t *testing.T) {
	e := newAddEnv(t, true, 0)
	e.loadThread("67494", "67494.html", "59416.guest.html")
	e.f95.SetValidUser("someone-else") // stored cookie gets the logged-out page
	rec := e.fetch("f95_thread", "67494", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "details pending") || strings.Contains(body, `name="nl.`) {
		t.Fatalf("pending fetch: %d\n%s", rec.Code, body)
	}
	form := confirmForm(t, body)
	if rec := e.post("/games", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM source WHERE details_pending = 1 AND external_id = '67494'`); n != 1 {
		t.Error("source not details_pending")
	}
	if n := e.count(`SELECT COUNT(*) FROM detail_fetch_queue WHERE reason = 'added' AND budget = 'routine'`); n != 1 {
		t.Error("detail fetch not queued")
	}
	if n := e.count(`SELECT COUNT(*) FROM game_tag`); n != 0 {
		t.Errorf("tags = %d", n)
	}
}

func TestAddGuestPageUsesGuestData(t *testing.T) {
	e := newAddEnv(t, false, 0) // no stored cookie: the thread reads as a guest
	e.loadThread("59416", "59416.guest.html", "59416.guest.html")
	rec := e.fetch("f95_thread", "59416", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "details pending") {
		t.Fatalf("%d\n%s", rec.Code, body)
	}
	if rec := e.post("/games", confirmForm(t, body)); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm %d", rec.Code)
	}
	var name string
	var ver sql.NullString
	if err := e.raw.QueryRow(`SELECT g.name, s.latest_version FROM game g JOIN source s ON s.game_id = g.id`).Scan(&name, &ver); err != nil {
		t.Fatal(err)
	}
	if name == "" || strings.HasPrefix(name, "F95 thread") || !ver.Valid {
		t.Errorf("guest data not used: %q %v", name, ver)
	}
	if e.count(`SELECT COUNT(*) FROM detail_fetch_queue`) != 1 {
		t.Error("detail fetch not queued")
	}
}

func TestAddItchAndManual(t *testing.T) {
	e := newAddEnv(t, true, 0)
	page, err := os.ReadFile("../itch/testdata/s1.html")
	if err != nil {
		t.Fatal(err)
	}
	e.itch.Set("/lycoris-radiata", page)
	rec := e.fetch("itchio", "https://Kuro-Kai.itch.io/lycoris-radiata/?x=1", url.Values{"tags": {"Harem, Yuri"}})
	if rec.Code != 200 {
		t.Fatalf("itch fetch %d\n%s", rec.Code, rec.Body.String())
	}
	form := confirmForm(t, rec.Body.String())
	acceptAll(form)
	if rec := e.post("/games", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("itch confirm %d %s", rec.Code, rec.Body.String())
	}
	var name, ext, url string
	var key sql.NullString
	if err := e.raw.QueryRow(`SELECT g.name, s.external_id, s.url, s.change_key FROM game g JOIN source s ON s.game_id = g.id WHERE s.kind = 'itchio'`).Scan(&name, &ext, &url, &key); err != nil {
		t.Fatal(err)
	}
	if name != "Lycoris Radiata" || ext != "kuro-kai.itch.io/lycoris-radiata" || url != "https://kuro-kai.itch.io/lycoris-radiata" || !strings.HasPrefix(key.String, "updated=") {
		t.Errorf("itch game = %q %q %q %q", name, ext, url, key.String)
	}
	if e.count(`SELECT COUNT(*) FROM game_tag`) == 0 {
		t.Error("pasted tag text produced no tags")
	}
	if rec := e.fetch("itchio", "https://kuro-kai.itch.io/lycoris-radiata", nil); rec.Code != http.StatusConflict {
		t.Errorf("tracked itch: %d", rec.Code)
	}

	rec = e.fetch("manual", "https://example.org/my-game", url2("name", "My Game", "version", "Ch. 2", "tags", "Harem"))
	if rec.Code != 200 {
		t.Fatalf("manual fetch %d", rec.Code)
	}
	form = confirmForm(t, rec.Body.String())
	form.Set("name", "My Game")
	acceptAll(form)
	if rec := e.post("/games", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("manual confirm %d %s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM source s JOIN game g ON g.id = s.game_id WHERE g.name = 'My Game' AND s.kind = 'manual' AND s.latest_version = 'Ch. 2' AND s.change_key IS NULL AND s.external_id IS NULL`) != 1 {
		t.Error("manual game/source not as expected")
	}
	if e.count(`SELECT COUNT(*) FROM game_tag gt JOIN source s ON s.game_id = gt.game_id WHERE s.kind = 'manual' AND gt.verification = 'unverified'`) == 0 {
		t.Error("manual pasted tags missing")
	}
	if n := len(e.f95.Requests()); n != 0 {
		t.Errorf("F95 requests = %d", n)
	}
}

func TestAddF95Busy(t *testing.T) {
	e := newAddEnv(t, true, 60*time.Millisecond)
	e.loadThread("67494", "67494.html", "67494.html")
	f, err := os.OpenFile(e.lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	rec := e.fetch("f95_thread", "67494", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "F95 busy, try again") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM game`); n != 0 {
		t.Errorf("games = %d", n)
	}
}

func TestAddHtmxReturnsOnlyPanels(t *testing.T) {
	e := newAddEnv(t, true, 0)
	e.loadThread("67494", "67494.html", "67494.html")
	req := httptest.NewRequest("POST", "/games/fetch", strings.NewReader(url.Values{"kind": {"f95_thread"}, "input": {"67494"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", e.cookie)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="parse-panels"`) || strings.Contains(body, "<html") {
		t.Fatalf("not a bare fragment:\n%.300s", body)
	}
}

func findKey(t *testing.T, f url.Values, raw, prefix string) string {
	t.Helper()
	for k, v := range f {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".raw") && v[0] == raw {
			return k
		}
	}
	t.Fatalf("no form row for %q", raw)
	return ""
}

func url2(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func genreKey(s string) string { return genre.Key(s) }
