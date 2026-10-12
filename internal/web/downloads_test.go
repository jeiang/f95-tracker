package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type dlEnv struct {
	t     *testing.T
	s     *Server
	store *db.Store
	h     http.Handler
	hdr   map[string]string
}

func newDLEnv(t *testing.T) *dlEnv {
	t.Helper()
	oidc := testutil.NewOIDCFake(t)
	store := testutil.NewStore(t)
	s := newServer(t, store, oidc)
	h, c := signedIn(t, s, oidc)
	return &dlEnv{t: t, s: s, store: store, h: h, hdr: map[string]string{"Cookie": c.Name + "=" + c.Value}}
}

func (e *dlEnv) req(method, target string, htmx bool, kv ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range e.hdr {
		r.Header.Set(k, v)
	}
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *dlEnv) game(name, thread, version string) int64 {
	g, _ := testutil.InsertGame(e.t, e.store, testutil.GameSpec{Name: name, ExternalID: thread, LatestVersion: version, PlayStatus: domain.PlayPlaying})
	return g.ID
}

var ctxBG = context.Background()

func TestDownloadPagesRequireSession(t *testing.T) {
	e := newDLEnv(t)
	for _, p := range []string{"/downloads", "/downloads/1/links"} {
		if rec := do(e.h, "GET", p, nil); rec.Code != http.StatusFound {
			t.Errorf("%s without session: %d", p, rec.Code)
		}
	}
	if rec := do(e.h, "POST", "/downloads/1/cancel", nil); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
		t.Errorf("cancel without session: %d %v", rec.Code, rec.Header())
	}
}

func TestDownloadButtonCreatesOneJobAndOpensPastePage(t *testing.T) {
	e := newDLEnv(t)
	id := e.game("Alpha", "1001", "v2.5")
	detail := "/games/" + itoa64(id)

	page := e.req("GET", detail, false).Body.String()
	if !strings.Contains(page, "Download v2.5") || !strings.Contains(page, `name="platform_pref"`) {
		t.Fatalf("detail lacks the Download controls")
	}
	rec := e.req("POST", detail+"/download", false)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !regexp.MustCompile(`^/downloads/\d+/links$`).MatchString(loc) {
		t.Fatalf("download: %d %q", rec.Code, loc)
	}
	again := e.req("POST", detail+"/download", false)
	if again.Header().Get("Location") != loc {
		t.Fatalf("second click opened %q, want the same job %q", again.Header().Get("Location"), loc)
	}
	if jobs, _ := e.s.downloads.List(ctxBG); len(jobs) != 1 || jobs[0].Trigger != downloads.TriggerButton {
		t.Fatalf("jobs: %+v", jobs)
	}
	// The detail page now links to the open job instead of a new one.
	if page := e.req("GET", detail, false).Body.String(); !strings.Contains(page, loc) || !strings.Contains(page, "Awaiting links") {
		t.Errorf("detail does not show the open job")
	}
	if rec := e.req("POST", "/games/9999/download", false); rec.Code != 404 {
		t.Errorf("unknown game: %d", rec.Code)
	}
	// htmx gets a client-side redirect instead of a 303 body.
	if rec := e.req("POST", detail+"/download", true); rec.Header().Get("HX-Redirect") != loc {
		t.Errorf("htmx redirect: %v", rec.Header())
	}
}

func TestPlatformPrefSelect(t *testing.T) {
	e := newDLEnv(t)
	id := e.game("Alpha", "1001", "v1")
	path := "/games/" + itoa64(id) + "/platform-pref"
	if rec := e.req("POST", path, true, "platform_pref", "win"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `<option value="win" selected>`) {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	if g, _ := e.store.Queries().GetGame(ctxBG, id); g.PlatformPref.String != "win" {
		t.Fatalf("stored %+v", g.PlatformPref)
	}
	if rec := e.req("POST", path, false, "platform_pref", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("clear: %d", rec.Code)
	}
	if g, _ := e.store.Queries().GetGame(ctxBG, id); g.PlatformPref.Valid {
		t.Fatalf("override not cleared: %+v", g.PlatformPref)
	}
	if rec := e.req("POST", path, true, "platform_pref", "beos"); rec.Code != 422 {
		t.Fatalf("invalid: %d", rec.Code)
	}
}

func TestPastePageAcceptsLinksAndRejectsMasked(t *testing.T) {
	e := newDLEnv(t)
	id := e.game("Alpha", "1001", "v2.5")
	j, _, err := e.s.downloads.Create(ctxBG, id)
	if err != nil {
		t.Fatal(err)
	}
	path := "/downloads/" + itoa64(j.ID) + "/links"
	if page := e.req("GET", path, false).Body.String(); !strings.Contains(page, "Alpha") || !strings.Contains(page, "<textarea") {
		t.Fatalf("paste page: %s", page)
	}

	masked := "https://pixeldrain.com/u/ok\nhttps://f95zone.to/masked/abc123/"
	rec := e.req("POST", path, false, "links", masked)
	body := rec.Body.String()
	if rec.Code != 422 || !strings.Contains(body, "Masked F95 links") || !strings.Contains(body, "f95zone.to/masked/abc123/") {
		t.Fatalf("masked: %d %s", rec.Code, body)
	}
	if got, _ := e.s.downloads.Get(ctxBG, j.ID); got.State != downloads.AwaitingLinks || len(got.Mirrors) != 0 {
		t.Fatalf("masked submission changed the job: %+v", got)
	}
	if rec := e.req("POST", path, false, "links", "not a url"); rec.Code != 422 {
		t.Fatalf("garbage: %d", rec.Code)
	}

	ok := "https://mega.nz/file/x#k Win/Linux\nhttps://pixeldrain.com/u/lin linux\n\nhttps://gofile.io/d/g\n"
	rec = e.req("POST", path, false, "links", ok, "platform", "win")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/downloads" {
		t.Fatalf("save: %d %v", rec.Code, rec.Header())
	}
	got, _ := e.s.downloads.Get(ctxBG, j.ID)
	if got.State != downloads.Queued || len(got.Mirrors) != 3 {
		t.Fatalf("stored: %+v", got)
	}
	var seen []string
	for _, m := range got.Mirrors {
		seen = append(seen, m.Host+"/"+m.Platform)
	}
	// pixeldrain "linux" first (explicit), then mega Win/Linux (guessed from the label), then gofile
	// with the select's "win".
	if want := "pixeldrain/linux mega/win_linux gofile/win"; strings.Join(seen, " ") != want {
		t.Fatalf("mirrors %v, want %s", seen, want)
	}
	// A queued job no longer takes links.
	if rec := e.req("POST", path, false, "links", "https://pixeldrain.com/u/z"); rec.Code != 409 {
		t.Fatalf("paste into queued: %d", rec.Code)
	}
	if page := e.req("GET", "/downloads", false).Body.String(); !strings.Contains(page, "Alpha") || !strings.Contains(page, "pixeldrain") || !strings.Contains(page, "Queued") {
		t.Fatalf("downloads page: %s", page)
	}
}

func TestDownloadActions(t *testing.T) {
	e := newDLEnv(t)
	svc := e.s.downloads
	mk := func(name, thread string) downloads.Job {
		j, _, err := svc.Create(ctxBG, e.game(name, thread, "v1"))
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	stuck := mk("Stuck", "1")
	if _, err := svc.IngestLinks(ctxBG, stuck.ID, []downloads.LinkInput{{URL: "https://workupload.com/f"}, {URL: "https://mega.nz/file/a#k"}}); err != nil {
		t.Fatal(err)
	}
	m, _ := svc.Get(ctxBG, stuck.ID)
	for _, mir := range m.Mirrors {
		if mir.Supported {
			id := mir.ID
			if _, err := svc.ApplyEvent(ctxBG, stuck.ID, downloads.Event{MirrorID: &id, MirrorState: "failed"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	page := e.req("GET", "/downloads", false).Body.String()
	for _, want := range []string{"Needs you", "all mirrors failed", "Retry", "Mark done", "Cancel", "Paste links"} {
		if !strings.Contains(page, want) {
			t.Errorf("needs_human job lacks %q", want)
		}
	}

	// htmx actions re-render the list fragment; retry queues it again.
	rec := e.req("POST", "/downloads/"+itoa64(stuck.ID)+"/retry", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Queued") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.req("POST", "/downloads/"+itoa64(stuck.ID)+"/done", true); rec.Code != 409 {
		t.Fatalf("done on queued: %d", rec.Code)
	}
	if rec := e.req("POST", "/downloads/"+itoa64(stuck.ID)+"/explode", false); rec.Code != 404 {
		t.Fatalf("unknown action: %d", rec.Code)
	}
	// Plain form: PRG back to the list.
	rec = e.req("POST", "/downloads/"+itoa64(stuck.ID)+"/cancel", false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/downloads" {
		t.Fatalf("cancel: %d %v", rec.Code, rec.Header())
	}
	if got, _ := svc.Get(ctxBG, stuck.ID); got.State != downloads.Cancelled {
		t.Fatalf("state %s", got.State)
	}
}

var tokenRE = regexp.MustCompile(`f95t_[A-Za-z0-9_-]{43}`)

func TestSettingsTokensShownOnceAndUsableOverAPI(t *testing.T) {
	e := newDLEnv(t)
	if page := e.req("GET", "/settings", false).Body.String(); !strings.Contains(page, "No API tokens") {
		t.Fatal("settings lacks the token section")
	}
	rec := e.req("POST", "/settings/tokens", false, "name", "artemis", "scope", "downloader")
	plain := tokenRE.FindString(rec.Body.String())
	if rec.Code != 200 || plain == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %v", rec.Code, rec.Header())
	}
	// The plaintext never appears again.
	if page := e.req("GET", "/settings", false).Body.String(); tokenRE.MatchString(page) || !strings.Contains(page, "artemis") || !strings.Contains(page, "never used") {
		t.Fatalf("reload shows the token or lacks the row")
	}

	api := func(token string) int {
		r := httptest.NewRequest("GET", "/api/v1/downloader/jobs", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r) // no session cookie: the bearer token alone authenticates
		return rec.Code
	}
	if api(plain) != 200 || api("") != 401 {
		t.Fatal("token not accepted, or API open without one")
	}
	if page := e.req("GET", "/settings", false).Body.String(); strings.Contains(page, "never used") {
		t.Error("last used not shown after use")
	}

	toks, _ := e.s.tokens.List(ctxBG)
	if rec := e.req("POST", "/settings/tokens/"+itoa64(toks[0].ID)+"/revoke", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if api(plain) != 401 {
		t.Fatal("revoked token still works")
	}
	for _, kv := range [][]string{{"name", "", "scope", "downloader"}, {"name", "x", "scope", "root"}} {
		if rec := e.req("POST", "/settings/tokens", true, kv...); rec.Code != 422 {
			t.Errorf("%v: %d", kv, rec.Code)
		}
	}
}

func TestSettingsDownloadPrefs(t *testing.T) {
	e := newDLEnv(t)
	rec := e.req("POST", "/settings/downloads", false, "host_order", "Gofile, pixeldrain", "platform_pref", "win_linux")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", rec.Code)
	}
	set, err := e.s.games.Settings(ctxBG)
	if err != nil || strings.Join(set.MirrorHostOrder, ",") != "gofile,pixeldrain" || set.PlatformPref != domain.PlatformWinLinux {
		t.Fatalf("stored %+v %v", set, err)
	}
	if page := e.req("GET", "/settings", false).Body.String(); !strings.Contains(page, `value="gofile, pixeldrain"`) {
		t.Error("prefs not shown")
	}
	for _, kv := range [][]string{
		{"host_order", "pixeldrain, zippyshare", "platform_pref", "linux"},
		{"host_order", "mega, mega", "platform_pref", "linux"},
		{"host_order", "mega", "platform_pref", "beos"},
	} {
		if rec := e.req("POST", "/settings/downloads", true, kv...); rec.Code != 422 {
			t.Errorf("%v: %d", kv, rec.Code)
		}
	}
	if set, _ := e.s.games.Settings(ctxBG); strings.Join(set.MirrorHostOrder, ",") != "gofile,pixeldrain" {
		t.Errorf("rejected save changed the settings: %+v", set)
	}
}
