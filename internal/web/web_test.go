package web

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/testutil"
	"github.com/jeiang/f95-tracker/internal/web/static"
)

func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	store := testutil.NewStore(t)
	return newServer(t, store, testutil.NewOIDCFake(t)), func() { store.Close() }
}

func newTestConfig() config.Config { return config.Config{} }

func newServer(t *testing.T, store *db.Store, fake *testutil.OIDCFake) *Server {
	t.Helper()
	return newServerWithConfig(t, store, fake.Apply(newTestConfig()))
}

func newServerWithConfig(t *testing.T, store *db.Store, cfg config.Config) *Server {
	t.Helper()
	clk := clock.NewFake(time.Unix(0, 0))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(Deps{Store: store, Clock: clk, Log: log, Config: cfg, Auth: auth.New(cfg, store, clk, log),
		Games: games.New(store, clk, games.Options{StateDir: t.TempDir()}), Tags: tags.New(store, clk)})
}

// signedIn returns a handler and a session cookie for it.
func signedIn(t *testing.T, s *Server, fake *testutil.OIDCFake) (http.Handler, *http.Cookie) {
	h := s.Handler()
	return h, testutil.LoginSession(t, h, fake)
}

func do(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthzFollowsDatabase(t *testing.T) {
	s, closeStore := newTestServer(t)
	h := s.Handler()
	if rec := do(h, "GET", "/healthz", nil); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthy: %d %q", rec.Code, rec.Body.String())
	}
	closeStore()
	if rec := do(h, "GET", "/healthz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed db: %d", rec.Code)
	}
}

func TestRootRedirectsAndGamesRendersLayout(t *testing.T) {
	fake := testutil.NewOIDCFake(t)
	h, c := signedIn(t, newServer(t, testutil.NewStore(t), fake), fake)
	hdr := map[string]string{"Cookie": c.Name + "=" + c.Value}
	if rec := do(h, "GET", "/", hdr); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/games" {
		t.Fatalf("/: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec := do(h, "GET", "/games", hdr)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, "/settings") {
		t.Fatalf("/games: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "/import-review") {
		t.Fatal("Import review tab shown without rows")
	}
}

func TestErrorMappingAndHTMXResponder(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []struct {
		err    error
		status int
	}{
		{fmt.Errorf("game 3: %w", domain.ErrNotFound), 404},
		{fmt.Errorf("%w: dup", domain.ErrConflict), 409},
		{domain.ErrIllegalTransition, 409},
		{fmt.Errorf("rating: %w", domain.ErrValidation), 422},
		{errors.New("boom secret detail"), 500},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/games/1/rating", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		s.respondError(rec, req, c.err)
		if rec.Code != c.status || rec.Header().Get("HX-Retarget") != "#error-slot" || rec.Header().Get("HX-Reswap") == "" {
			t.Errorf("%v: %d %v", c.err, rec.Code, rec.Header())
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("htmx error must be a fragment: %s", rec.Body.String())
		}
		if c.status == 500 && strings.Contains(rec.Body.String(), "secret") {
			t.Error("500 leaked error text")
		}
	}
	// Plain browser request: full page, no HX headers.
	rec := httptest.NewRecorder()
	s.respondError(rec, httptest.NewRequest("GET", "/games/9", nil), domain.ErrNotFound)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "<html") || rec.Header().Get("HX-Retarget") != "" {
		t.Fatalf("page error: %d %v", rec.Code, rec.Header())
	}
	// API: JSON contract.
	rec = httptest.NewRecorder()
	s.respondError(rec, httptest.NewRequest("GET", "/api/v1/x", nil), domain.ErrIllegalTransition)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"error":"illegal_transition"`) {
		t.Fatalf("api error: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPanicBecomes500(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }))
	if rec := do(h, "GET", "/", nil); rec.Code != 500 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	rec := do(h, "POST", "/games", map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", rec.Code)
	}
}

func TestStaticServing(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	u := static.URL("htmx.min.js")
	if !strings.Contains(u, "?v=") {
		t.Fatalf("no hash in %q", u)
	}
	rec := do(h, "GET", u, nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("hashed asset: %d %v", rec.Code, rec.Header())
	}
	if rec := do(h, "GET", "/static/htmx.min.js", nil); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("unhashed must revalidate: %v", rec.Header())
	}
	for _, p := range []string{"/static/", "/static/fonts/", "/static/nope.js"} {
		if rec := do(h, "GET", p, nil); rec.Code != 404 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}
