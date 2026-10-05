package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type setEnv struct {
	t      *testing.T
	store  *db.Store
	h      http.Handler
	cookie *http.Cookie
	f95    *testutil.F95Fake
	ntfy   *testutil.NtfyFake
	creds  *f95.CredStore
	tags   *tags.Service
	games  *games.Service
}

const (
	loggedInHTML = `<html data-logged-in="true"><body>ok</body></html>`
	guestHTML    = `<html data-logged-in="false"><body>login</body></html>`
)

func newSetEnv(t *testing.T) *setEnv {
	t.Helper()
	store := testutil.NewStore(t)
	oidc := testutil.NewOIDCFake(t)
	e := &setEnv{t: t, store: store, f95: testutil.NewF95Fake(t), ntfy: testutil.NewNtfyFake(t)}
	clk := clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC))
	e.creds = f95.NewCredStore(store, clk)
	client, err := f95.New(f95.Options{BaseURL: e.f95.URL(), Creds: e.creds, Version: "test", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	e.tags = tags.New(store, clk)
	e.games = games.New(store, clk, games.Options{StateDir: t.TempDir()})
	cfg := oidc.Apply(newTestConfig())
	cfg.NtfyURL, cfg.NtfyTopic = e.ntfy.URL, "f95-test"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Deps{Store: store, Clock: clk, Log: log, Config: cfg, Auth: auth.New(cfg, store, clk, log),
		Games: e.games, Tags: e.tags, F95: client, Notify: notify.New(store, clk, notify.Options{URL: e.ntfy.URL, Topic: "f95-test"})})
	e.h, e.cookie = signedIn(t, s, oidc)
	return e
}

func (e *setEnv) req(method, target string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.AddCookie(e.cookie)
	r.Header.Set("User-Agent", "TestBrowser/1")
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func TestSaveCookie(t *testing.T) {
	const secret = "uservalue-zz9"
	t.Run("valid cookie clears the invalid state and is never echoed", func(t *testing.T) {
		e := newSetEnv(t)
		ctx := context.Background()
		e.f95.SetValidUser(secret)
		e.f95.SetThread("67494", []byte(loggedInHTML), []byte(guestHTML))
		if err := e.creds.Replace(ctx, f95.Jar{"xf_user": "old", "xf_session": "s"}, "UA", time.Time{}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.creds.MarkInvalid(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.creds.MarkAlerted(ctx); err != nil {
			t.Fatal(err)
		}
		if body := e.req("GET", "/games", nil, false).Body.String(); !strings.Contains(body, "no longer valid") {
			t.Fatal("invalid banner missing before the save")
		}

		rec := e.req("POST", "/settings/cookie", url.Values{
			"cookie": {"Cookie: xf_user=" + secret + "; xf_tfa_trust=trust-" + secret}, "tfa_expires": {"2026-10-26"},
		}, true)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `id="cookie-status"`) || !strings.Contains(body, "valid") || strings.Contains(body, "<html") {
			t.Fatalf("htmx save: %d %s", rec.Code, body)
		}
		if strings.Contains(body, secret) {
			t.Fatal("cookie value echoed in the response")
		}
		row, err := e.creds.Get(ctx)
		if err != nil || row.Validity != "valid" || row.TfaTrustExpiresAt.String != "2026-10-26" || row.InvalidAlertedAt.Valid || row.UserAgent != "TestBrowser/1" {
			t.Fatalf("row = %+v %v", row, err)
		}
		if body := e.req("GET", "/games", nil, false).Body.String(); strings.Contains(body, "no longer valid") {
			t.Error("invalid banner still shown after a valid save")
		}
		page := e.req("GET", "/settings", nil, false).Body.String()
		if strings.Contains(page, secret) || !strings.Contains(page, "2026-10-26") {
			t.Error("settings page must show the expiry and never the cookie")
		}
	})

	t.Run("invalid cookie is stored as invalid", func(t *testing.T) {
		e := newSetEnv(t)
		e.f95.SetValidUser("someone-else")
		e.f95.SetThread("67494", []byte(loggedInHTML), []byte(guestHTML))
		rec := e.req("POST", "/settings/cookie", url.Values{"cookie": {"xf_user=" + secret + "; xf_session=s1"}}, false)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, "chip-bad") || !strings.Contains(body, "<html") {
			t.Fatalf("plain save: %d %s", rec.Code, body)
		}
		if strings.Contains(body, secret) {
			t.Fatal("cookie value echoed in the response")
		}
		if row, _ := e.creds.Get(context.Background()); row.Validity != "invalid" {
			t.Errorf("validity = %q", row.Validity)
		}
		if body := e.req("GET", "/games", nil, false).Body.String(); !strings.Contains(body, "no longer valid") {
			t.Error("banner missing after an invalid save")
		}
	})

	t.Run("unusable input is rejected without touching F95 or the stored cookie", func(t *testing.T) {
		e := newSetEnv(t)
		for name, form := range map[string]url.Values{
			"empty":        {"cookie": {"  "}},
			"missing keys": {"cookie": {"xf_user=" + secret}},
			"bad date":     {"cookie": {"xf_user=u; xf_session=s"}, "tfa_expires": {"26/10/2026"}},
		} {
			rec := e.req("POST", "/settings/cookie", form, true)
			if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("HX-Retarget") != "#error-slot" || strings.Contains(rec.Body.String(), secret) {
				t.Errorf("%s: %d retarget=%q body=%s", name, rec.Code, rec.Header().Get("HX-Retarget"), rec.Body)
			}
		}
		if _, ok, _ := e.creds.Load(context.Background()); ok || len(e.f95.Requests()) != 0 {
			t.Error("rejected input stored a cookie or hit F95")
		}
	})

	t.Run("F95 failure keeps the cookie and says so", func(t *testing.T) {
		e := newSetEnv(t)
		e.f95.SetThread("67494", []byte(loggedInHTML), []byte(guestHTML))
		e.f95.Program("67494", testutil.F95Repeat(testutil.F95Status(503), 5)...)
		rec := e.req("POST", "/settings/cookie", url.Values{"cookie": {"xf_user=u1; xf_session=s1"}}, true)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "blocking requests") {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if row, err := e.creds.Get(context.Background()); err != nil || row.Validity == "valid" {
			t.Errorf("row = %+v %v", row, err)
		}
	})
}

func TestAlertSet(t *testing.T) {
	e := newSetEnv(t)
	for _, ps := range []domain.PlayStatus{domain.PlayPlaying, domain.PlayPlaying, domain.PlayFinished, domain.PlayDropped} {
		testutil.InsertGame(t, e.store, testutil.GameSpec{PlayStatus: ps})
	}
	page := e.req("GET", "/settings", nil, false).Body.String()
	// default set: playing, on hold, planned -> 2 of 4 Games alert
	if !strings.Contains(page, "2 of 4 Games currently alert") {
		t.Fatalf("default counts missing: %s", page)
	}

	rec := e.req("POST", "/settings/alert-set", url.Values{"status": {"finished", "dropped"}}, true)
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, "<html") || !strings.Contains(body, "Saved") || !strings.Contains(body, "2 of 4 Games currently alert") {
		t.Fatalf("htmx save: %d %s", rec.Code, body)
	}
	set, _ := e.games.AlertSet(context.Background())
	if len(set) != 2 {
		t.Fatalf("set = %v", set)
	}
	rec = e.req("POST", "/settings/alert-set", url.Values{"status": {"nonsense"}}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid status: %d", rec.Code)
	}
	e.req("POST", "/settings/alert-set", url.Values{}, false)
	if set, _ := e.games.AlertSet(context.Background()); len(set) != 0 {
		t.Errorf("empty set must be allowed, got %v", set)
	}
}

func TestSynonymEditor(t *testing.T) {
	e := newSetEnv(t)
	ctx := context.Background()
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{})
	m, err := e.tags.ParseCheck(ctx, "Threesome", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return e.tags.ApplyAddTx(ctx, q, g.ID, m) }); err != nil {
		t.Fatal(err)
	}

	// create: the unverified Custom tag "Threesome" is re-pointed to the F95 tag
	rec := e.req("POST", "/settings/synonyms", url.Values{"phrase": {"Threesome"}, "target": {"harem"}, "kind": {"f95"}}, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `id="synonym-list"`) || strings.Contains(body, "<html") || !strings.Contains(body, "Re-pointed 1 Game tag") {
		t.Fatalf("create: %d %s", rec.Code, body)
	}
	rows, _ := e.tags.ListSynonyms(ctx, "threesome")
	if len(rows) != 1 || rows[0].Tag.Slug != "harem" || rows[0].Origin != "user" {
		t.Fatalf("rows = %+v", rows)
	}

	// duplicate, unknown F95 tag, empty phrase
	for name, form := range map[string]url.Values{
		"duplicate":    {"phrase": {"threesome"}, "target": {"harem"}, "kind": {"f95"}},
		"unknown f95":  {"phrase": {"zzz"}, "target": {"no-such-tag"}, "kind": {"f95"}},
		"empty phrase": {"phrase": {"!!!"}, "target": {"harem"}, "kind": {"f95"}},
	} {
		if rec := e.req("POST", "/settings/synonyms", form, true); rec.Code < 400 || rec.Header().Get("HX-Retarget") != "#error-slot" {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if all, _ := e.tags.Tags(ctx); len(all) > 0 {
		for _, tg := range all {
			if tg.Slug == "no-such-tag" {
				t.Error("a typo minted an F95 tag")
			}
		}
	}

	// edit: point it at a Custom tag; the tag on the Game follows
	id := i64s(rows[0].ID)
	rec = e.req("POST", "/settings/synonyms/"+id, url.Values{"phrase": {"Threesome"}, "target": {"Trio"}, "kind": {"custom"}}, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Re-pointed 1 Game tag") {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	gt, _ := e.tags.GameTags(ctx, g.ID)
	if len(gt) != 1 || gt[0].TagLabel != "Trio" {
		t.Fatalf("game tags = %+v", gt)
	}
	if rec := e.req("POST", "/settings/synonyms/99999", url.Values{"phrase": {"x"}, "target": {"Trio"}, "kind": {"custom"}}, true); rec.Code != 404 {
		t.Errorf("edit missing: %d", rec.Code)
	}

	// filter (htmx fragment) and the page
	rec = e.req("GET", "/settings?syn=threesome", nil, false)
	if !strings.Contains(rec.Body.String(), `value="Trio"`) {
		t.Errorf("page missing the synonym row")
	}
	r := httptest.NewRequest("GET", "/settings?syn=nomatchhere", nil)
	r.AddCookie(e.cookie)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Target", "synonym-list")
	frec := httptest.NewRecorder()
	e.h.ServeHTTP(frec, r)
	if fb := frec.Body.String(); strings.Contains(fb, "<html") || !strings.Contains(fb, "No Synonyms match") || strings.Contains(fb, `value="Trio"`) {
		t.Errorf("filter fragment: %s", fb)
	}

	// delete: htmx DELETE and no-JS POST
	if rec := e.req("DELETE", "/settings/synonyms/"+id, nil, true); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rows, _ := e.tags.ListSynonyms(ctx, "threesome"); len(rows) != 0 {
		t.Fatalf("not deleted: %+v", rows)
	}
	row, _, err := e.tags.CreateSynonym(ctx, "Big Booty", tags.TagRef{Kind: tags.KindF95, Slug: "big-ass"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.req("POST", "/settings/synonyms/"+i64s(row.ID)+"/delete", url.Values{}, false); rec.Code != 200 {
		t.Fatalf("post delete: %d", rec.Code)
	}
	if rec := e.req("DELETE", "/settings/synonyms/"+id, nil, true); rec.Code != 404 {
		t.Errorf("delete twice: %d", rec.Code)
	}
}

func TestNtfyTest(t *testing.T) {
	e := newSetEnv(t)
	page := e.req("GET", "/settings", nil, false).Body.String()
	if !strings.Contains(page, e.ntfy.URL) || !strings.Contains(page, "f95-test") {
		t.Error("ntfy server and topic missing from the page")
	}

	rec := e.req("POST", "/settings/ntfy/test", url.Values{}, true)
	reqs := e.ntfy.Requests()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Sent") || len(reqs) != 1 || reqs[0].Header.Get("Priority") != "3" {
		t.Fatalf("success: %d %s %+v", rec.Code, rec.Body, reqs)
	}

	e.ntfy.FailNext(1, 500)
	rec = e.req("POST", "/settings/ntfy/test", url.Values{}, true)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, "Failed") || !strings.Contains(body, "500") || strings.Contains(body, "<html") {
		t.Fatalf("failure: %d %s", rec.Code, body)
	}
}

func i64s(n int64) string { return strconv.FormatInt(n, 10) }
