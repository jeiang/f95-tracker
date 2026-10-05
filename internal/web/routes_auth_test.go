package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/testutil"
)

func cookieHdr(c *http.Cookie) map[string]string {
	return map[string]string{"Cookie": c.Name + "=" + c.Value}
}

func TestUnauthenticatedResponses(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	cases := []struct {
		name, method, target string
		hdr                  map[string]string
		status               int
		check                func(*httptest.ResponseRecorder) bool
	}{
		{"html get", "GET", "/games?q=a", nil, 302, func(r *httptest.ResponseRecorder) bool {
			return r.Header().Get("Location") == "/auth/login?next=%2Fgames%3Fq%3Da"
		}},
		{"htmx", "GET", "/games", map[string]string{"HX-Request": "true"}, 200, func(r *httptest.ResponseRecorder) bool {
			return r.Header().Get("HX-Redirect") == "/auth/login"
		}},
		{"api", "GET", "/api/v1/games", nil, 401, func(r *httptest.ResponseRecorder) bool {
			return strings.Contains(r.Body.String(), `"unauthorized"`) && strings.HasPrefix(r.Header().Get("Content-Type"), "application/json")
		}},
		{"unknown path is still guarded", "GET", "/nope", nil, 302, func(*httptest.ResponseRecorder) bool { return true }},
		{"healthz open", "GET", "/healthz", nil, 200, func(*httptest.ResponseRecorder) bool { return true }},
		{"static open", "GET", "/static/htmx.min.js", nil, 200, func(*httptest.ResponseRecorder) bool { return true }},
	}
	for _, c := range cases {
		rec := do(h, c.method, c.target, c.hdr)
		if rec.Code != c.status || !c.check(rec) {
			t.Errorf("%s: %d %v %s", c.name, rec.Code, rec.Header(), rec.Body.String())
		}
	}
}

func TestLoginFlow(t *testing.T) {
	fake := testutil.NewOIDCFake(t)
	store := testutil.NewStore(t)
	h := newServer(t, store, fake).Handler()

	// Start from a protected page; the session cookie works for it afterwards.
	rec := do(h, "GET", "/auth/login?next=%2Fgames", nil)
	if rec.Code != 302 {
		t.Fatalf("login: %d", rec.Code)
	}
	authz, _ := url.Parse(rec.Header().Get("Location"))
	q := authz.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" ||
		q.Get("redirect_uri") != "http://f95.test/auth/callback" {
		t.Fatalf("authorize params: %v", q)
	}
	var pre *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "f95_oidc" {
			pre = c
		}
	}
	if pre == nil || !pre.HttpOnly {
		t.Fatalf("pre-login cookie: %+v", pre)
	}

	cb := func(target string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return do(h, "GET", target, cookieHdr(cookie))
	}
	hop := func() string { // authorize at the fake, return the callback path+query
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Get(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		u, _ := url.Parse(resp.Header.Get("Location"))
		return u.RequestURI()
	}
	target := hop()

	// Tampered state, forged cookie and missing cookie are rejected.
	bad := strings.Replace(target, "state=", "state=x", 1)
	if r := cb(bad, pre); r.Code != 400 {
		t.Errorf("state mismatch: %d", r.Code)
	}
	forged := *pre
	forged.Value = pre.Value[:len(pre.Value)-2] + "AA"
	if r := cb(target, &forged); r.Code != 400 {
		t.Errorf("forged cookie: %d", r.Code)
	}
	if r := do(h, "GET", target, nil); r.Code != 400 {
		t.Errorf("no cookie: %d", r.Code)
	}

	// The real callback creates a session and lands on next.
	fresh := httptest.NewRecorder()
	h.ServeHTTP(fresh, func() *http.Request {
		r := httptest.NewRequest("GET", target, nil)
		r.AddCookie(pre)
		return r
	}())
	if fresh.Code != 302 || fresh.Header().Get("Location") != "/games" {
		t.Fatalf("callback: %d %q", fresh.Code, fresh.Header().Get("Location"))
	}
	var sess *http.Cookie
	for _, c := range fresh.Result().Cookies() {
		if c.Name == "f95_session" {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.Secure {
		t.Fatalf("session cookie: %+v", sess)
	}
	if n, _ := store.Queries().CountSessions(t.Context()); n != 1 {
		t.Fatalf("sessions rows: %d", n)
	}
	if r := do(h, "GET", "/games", cookieHdr(sess)); r.Code != 200 {
		t.Fatalf("with session: %d", r.Code)
	}

	// The session survives a new Server over the same database.
	other := newServer(t, store, fake).Handler()
	if r := do(other, "GET", "/games", cookieHdr(sess)); r.Code != 200 {
		t.Fatalf("new server: %d", r.Code)
	}

	// Logout deletes the row and the cookie stops working.
	if r := do(h, "POST", "/auth/logout", cookieHdr(sess)); r.Code != 200 {
		t.Fatalf("logout: %d", r.Code)
	}
	if n, _ := store.Queries().CountSessions(t.Context()); n != 0 {
		t.Fatalf("rows after logout: %d", n)
	}
	if r := do(h, "GET", "/games", cookieHdr(sess)); r.Code != 302 {
		t.Fatalf("after logout: %d", r.Code)
	}
}

func TestLoginRejections(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*testutil.OIDCFake)
		status int
	}{
		{"subject not allowlisted", func(f *testutil.OIDCFake) {}, 403},
		{"nonce mismatch", func(f *testutil.OIDCFake) { f.SetBadNonce(true) }, 400},
	}
	for _, c := range cases {
		fake := testutil.NewOIDCFake(t)
		store := testutil.NewStore(t)
		s := newServer(t, store, fake) // allowlist holds the fake's initial sub
		c.setup(fake)
		if c.name == "subject not allowlisted" {
			fake.SetSub("someone-else")
		}
		h := s.Handler()
		rec := do(h, "GET", "/auth/login", nil)
		pre := rec.Result().Cookies()[0]
		cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := cl.Get(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		u, _ := url.Parse(resp.Header.Get("Location"))
		r := do(h, "GET", u.RequestURI(), cookieHdr(pre))
		if r.Code != c.status {
			t.Errorf("%s: %d", c.name, r.Code)
		}
		if n, _ := store.Queries().CountSessions(t.Context()); n != 0 {
			t.Errorf("%s: session created", c.name)
		}
	}
}

func TestEmptyAllowlistDeniesEveryone(t *testing.T) {
	fake := testutil.NewOIDCFake(t)
	store := testutil.NewStore(t)
	cfg := fake.Apply(newTestConfig())
	cfg.OIDCAllowedSubjects = nil
	h := newServerWithConfig(t, store, cfg).Handler()
	rec := do(h, "GET", "/auth/login", nil)
	pre := rec.Result().Cookies()[0]
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	u, _ := url.Parse(resp.Header.Get("Location"))
	if r := do(h, "GET", u.RequestURI(), cookieHdr(pre)); r.Code != 403 {
		t.Fatalf("empty allowlist: %d", r.Code)
	}
}

func TestLoginNextRejectsOpenRedirect(t *testing.T) {
	fake := testutil.NewOIDCFake(t)
	h := newServer(t, testutil.NewStore(t), fake).Handler()
	for _, next := range []string{"https://evil.example/", "//evil.example/x", `/\evil.example`, "javascript:alert(1)", "games"} {
		rec := do(h, "GET", "/auth/login?next="+url.QueryEscape(next), nil)
		cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := cl.Get(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		u, _ := url.Parse(resp.Header.Get("Location"))
		r := do(h, "GET", u.RequestURI(), cookieHdr(rec.Result().Cookies()[0]))
		if r.Code != 302 || r.Header().Get("Location") != "/" {
			t.Errorf("next %q -> %d %q", next, r.Code, r.Header().Get("Location"))
		}
	}
}

func TestStateChangingRequestsAreCrossOriginChecked(t *testing.T) {
	fake := testutil.NewOIDCFake(t)
	h, c := signedIn(t, newServer(t, testutil.NewStore(t), fake), fake)
	hdr := cookieHdr(c)
	hdr["Sec-Fetch-Site"] = "cross-site"
	if r := do(h, "POST", "/auth/logout", hdr); r.Code != 403 {
		t.Fatalf("cross-site logout: %d", r.Code)
	}
}

func TestServerStartsWithUnreachableIssuer(t *testing.T) {
	fake := testutil.NewOIDCFake(t)
	cfg := fake.Apply(newTestConfig())
	cfg.OIDCIssuer = "http://127.0.0.1:1"
	h := newServerWithConfig(t, testutil.NewStore(t), cfg).Handler()
	if r := do(h, "GET", "/healthz", nil); r.Code != 200 {
		t.Fatalf("healthz: %d", r.Code)
	}
	if r := do(h, "GET", "/auth/login", nil); r.Code != http.StatusBadGateway {
		t.Fatalf("login with dead issuer: %d", r.Code)
	}
}
