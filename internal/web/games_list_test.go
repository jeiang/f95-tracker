package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type listEnv struct {
	t     *testing.T
	store *db.Store
	raw   *sql.DB
	h     http.Handler
	hdr   map[string]string
}

var listNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newListEnv(t *testing.T) *listEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "f95-tracker.db")
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
	fake := testutil.NewOIDCFake(t)
	cfg := fake.Apply(newTestConfig())
	clk := clock.NewFake(listNow)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Deps{Store: store, Clock: clk, Log: log, Config: cfg, Auth: auth.New(cfg, store, clk, log), Games: games.New(store, clk, games.Options{})})
	h, c := signedIn(t, s, fake)
	return &listEnv{t: t, store: store, raw: raw, h: h, hdr: map[string]string{"Cookie": c.Name + "=" + c.Value}}
}

func (e *listEnv) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.raw.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *listEnv) tag(gameID int64, slug string) {
	e.t.Helper()
	e.sql(`INSERT INTO game_tag(game_id, tag_id, origin, qualifier) VALUES (?, (SELECT id FROM tag WHERE slug = ?), 'manual', 'present')`, gameID, slug)
}

func (e *listEnv) get(target string, htmx bool) (int, string) {
	e.t.Helper()
	hdr := map[string]string{}
	for k, v := range e.hdr {
		hdr[k] = v
	}
	if htmx {
		hdr["HX-Request"] = "true"
	}
	rec := do(e.h, "GET", target, hdr)
	return rec.Code, rec.Body.String()
}

func shown(body string, id int64) bool {
	return strings.Contains(body, fmt.Sprintf(`href="/games/%d"`, id))
}

func TestGameListFilters(t *testing.T) {
	e := newListEnv(t)
	alpha, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha Quest", PlayStatus: domain.PlayPlaying, RatingX2: 9, LatestVersion: "2.0", DevStatus: domain.DevOngoing})
	testutil.InsertPlayLog(t, e.store, alpha.ID, "1.0", "2026-02-01") // Behind: playing is in the alert set
	beta, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Beta Run", PlayStatus: domain.PlayFinished, LatestVersion: "1.0", DevStatus: domain.DevCompleted})
	testutil.InsertPlayLog(t, e.store, beta.ID, "0.5", "2026-02-01") // behind, but finished is not alerting
	gamma, gsrc := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Gamma Ray", PlayStatus: domain.PlayPlanned, RatingX2: 4, LatestVersion: "3.1"})
	e.sql(`INSERT INTO check_run(kind, started_at, finished_at, status) VALUES ('daily','2026-02-27T00:00:00Z','2026-02-27T00:01:00Z','ok')`)
	e.sql(`INSERT INTO check_result(run_id, source_id, step, outcome, old_key, new_key, at) VALUES ((SELECT MAX(id) FROM check_run), ?, 'checker', 'update', '3.0', '3.1', '2026-02-27T00:00:30Z')`, gsrc.ID)
	e.tag(alpha.ID, "adventure")
	e.tag(gamma.ID, "adventure")
	e.tag(gamma.ID, "2dcg")

	cases := []struct {
		name  string
		query string
		want  []int64
	}{
		{"no filters", "", []int64{alpha.ID, beta.ID, gamma.ID}},
		{"play status", "?play=finished", []int64{beta.ID}},
		{"name substring, case-insensitive", "?q=RAY", []int64{gamma.ID}},
		{"dev status", "?dev=ongoing", []int64{alpha.ID}},
		{"behind is alert-set only", "?behind=1", []int64{alpha.ID}},
		{"updates", "?updates=1", []int64{gamma.ID}},
		{"rating at least", "?rating=4.5", []int64{alpha.ID}},
		{"include tag", "?tag=2dcg", []int64{gamma.ID}},
		{"include two tags is AND", "?tag=adventure&tag=2dcg", []int64{gamma.ID}},
		{"exclude tag", "?tag=-2dcg", []int64{alpha.ID, beta.ID}},
		{"sort by name descending", "?sort=name&dir=desc&play=playing", []int64{alpha.ID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := e.get("/games"+c.query, false)
			if code != 200 {
				t.Fatalf("status %d", code)
			}
			for _, id := range []int64{alpha.ID, beta.ID, gamma.ID} {
				want := false
				for _, w := range c.want {
					want = want || w == id
				}
				if shown(body, id) != want {
					t.Errorf("game %d shown=%v want %v", id, !want, want)
				}
			}
		})
	}

	_, body := e.get("/games", false)
	for _, badge := range []string{"Behind", "Update"} {
		if !strings.Contains(body, ">"+badge+"<") {
			t.Errorf("missing %s badge", badge)
		}
	}
	// Default sort puts Games with an Update or Behind first (Gamma/Alpha before Beta).
	if i, j := strings.Index(body, fmt.Sprintf(`href="/games/%d"`, beta.ID)), strings.Index(body, fmt.Sprintf(`href="/games/%d"`, alpha.ID)); j > i {
		t.Error("default sort: Behind Game should precede the plain one")
	}
}

func TestGameListSortOrder(t *testing.T) {
	e := newListEnv(t)
	a, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Bravo"})
	b, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	order := func(q string) []int64 {
		_, body := e.get("/games"+q, false)
		if strings.Index(body, fmt.Sprintf(`href="/games/%d"`, a.ID)) < strings.Index(body, fmt.Sprintf(`href="/games/%d"`, b.ID)) {
			return []int64{a.ID, b.ID}
		}
		return []int64{b.ID, a.ID}
	}
	if got := order("?sort=name"); got[0] != b.ID {
		t.Errorf("name asc: %v", got)
	}
	if got := order("?sort=name&dir=desc"); got[0] != a.ID {
		t.Errorf("name desc: %v", got)
	}
}

func TestGameListHTMXReturnsFragmentOnly(t *testing.T) {
	e := newListEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	code, body := e.get("/games?q=alp", true)
	if code != 200 || strings.Contains(body, "<html") || strings.Contains(body, `aria-label="Primary"`) {
		t.Fatalf("htmx must get the fragment, got %d %.200s", code, body)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="game-table"`) || !shown(body, g.ID) {
		t.Fatalf("fragment: %s", body)
	}
	_, full := e.get("/games?q=alp", false)
	if !strings.Contains(full, "<html") || !strings.Contains(full, `id="game-table"`) || !strings.Contains(full, `id="filters"`) {
		t.Fatal("plain request must be the full page with the filter form")
	}
}

func TestGameListIgnoresInvalidParams(t *testing.T) {
	e := newListEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	for _, q := range []string{
		"?play=nope", "?dev=%00", "?rating=abc", "?rating=99", "?rating=4.3", "?sort=bogus", "?dir=sideways",
		"?tag=does-not-exist&tag=-also-missing", "?behind=yes&updates=2",
	} {
		code, body := e.get("/games"+q, false)
		if code != 200 || !shown(body, g.ID) {
			t.Errorf("%s: status %d, game shown=%v", q, code, shown(body, g.ID))
		}
	}
}

func TestGameListEmptyAndNoMatchStates(t *testing.T) {
	e := newListEnv(t)
	_, body := e.get("/games", false)
	if !strings.Contains(body, `id="first-run"`) || !strings.Contains(body, `href="/games/new"`) || !strings.Contains(body, "CSV") {
		t.Fatal("first-run state missing")
	}
	if strings.Contains(body, `id="no-match"`) || strings.Contains(body, `id="filters"`) {
		t.Fatal("first-run must not show the filter UI")
	}

	testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	_, body = e.get("/games?q=zzz", false)
	if !strings.Contains(body, `id="no-match"`) || !strings.Contains(body, "Clear filters") || strings.Contains(body, `id="first-run"`) {
		t.Fatal("no-match state missing")
	}
	_, body = e.get("/games", false)
	if strings.Contains(body, `id="no-match"`) || strings.Contains(body, `id="first-run"`) {
		t.Fatal("populated list shows an empty state")
	}
}

func TestGameListStripsBadgesAndImportTab(t *testing.T) {
	e := newListEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	_, body := e.get("/games", false)
	for _, absent := range []string{`id="strip-import-review"`, `id="strip-tag-queue"`, "/import-review", "check Play status", "tags to review"} {
		if strings.Contains(body, absent) {
			t.Errorf("%q shown with nothing to review", absent)
		}
	}

	e.sql(`UPDATE game SET import_review = 1 WHERE id = ?`, g.ID)
	e.sql(`INSERT INTO tag_review(game_id, state, updated_at) VALUES (?, 'skipped', '2026-01-01T00:00:00Z')`, g.ID)
	_, body = e.get("/games", false)
	for _, present := range []string{`id="strip-import-review"`, `id="strip-tag-queue"`, `href="/import-review"`, `href="/queue"`, "check Play status", "tags to review"} {
		if !strings.Contains(body, present) {
			t.Errorf("%q missing", present)
		}
	}

	e.sql(`UPDATE tag_review SET state = 'done', reviewed_at = '2026-01-02T00:00:00Z'`)
	_, body = e.get("/games", false)
	if strings.Contains(body, `id="strip-tag-queue"`) || strings.Contains(body, "tags to review") {
		t.Error("done tag review must leave the queue")
	}
}

func TestPageMetaBanners(t *testing.T) {
	e := newListEnv(t)
	testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	banners := func() string {
		_, body := e.get("/games", false)
		i := strings.Index(body, `id="banners"`)
		j := strings.Index(body, "<main")
		return body[i:j]
	}
	if b := banners(); strings.Contains(b, "<a ") || strings.Contains(b, "border-b") {
		t.Fatalf("banner without a condition: %s", b)
	}

	e.sql(`INSERT INTO f95_credential(id, cookie_jar, user_agent, validity, updated_at) VALUES (1,'{}','ua','invalid','x')`)
	if b := banners(); !strings.Contains(b, "cookie is no longer valid") || !strings.Contains(b, `href="/settings"`) {
		t.Errorf("cookie invalid banner: %s", b)
	}
	e.sql(`UPDATE f95_credential SET validity = 'valid'`)
	if b := banners(); strings.Contains(b, "border-b") {
		t.Errorf("valid cookie must not banner: %s", b)
	}

	for _, c := range []struct {
		expires string
		want    bool
	}{
		{"2026-03-10", true},           // within 14 days
		{"2026-03-10T00:00:00Z", true}, // timestamp form
		{"2026-02-20", true},           // already expired
		{"2026-04-01", false},          // far away
	} {
		e.sql(`UPDATE f95_credential SET tfa_trust_expires_at = ?`, c.expires)
		if got := strings.Contains(banners(), "two-step trust"); got != c.want {
			t.Errorf("tfa expiry %s: banner=%v want %v", c.expires, got, c.want)
		}
	}

	e.sql(`UPDATE f95_credential SET tfa_trust_expires_at = NULL`)
	for status, want := range map[string]bool{"ok": false, "running": false, "failed": true, "partial": true} {
		e.sql(`DELETE FROM check_run`)
		finished := any("2026-03-01T08:01:00Z")
		if status == "running" {
			finished = nil
		}
		e.sql(`INSERT INTO check_run(kind, started_at, finished_at, status) VALUES ('daily','2026-03-01T08:00:00Z', ?, ?)`, finished, status)
		if got := strings.Contains(banners(), "Source check"); got != want {
			t.Errorf("check run %s: banner=%v want %v", status, got, want)
		}
	}
}

func TestGameListTagChipsCycle(t *testing.T) {
	e := newListEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Alpha"})
	e.tag(g.ID, "adventure")
	next := func(query string) string {
		_, body := e.get("/games"+query, false)
		i := strings.Index(body, `aria-label="adventure, `)
		if i < 0 {
			t.Fatalf("no adventure chip: %s", query)
		}
		k := strings.LastIndex(body[:i], `href="`) + len(`href="`)
		return strings.ReplaceAll(body[k:k+strings.Index(body[k:], `"`)], "&amp;", "&")
	}
	if got := next(""); got != "/games?tag=adventure" {
		t.Errorf("none -> include: %s", got)
	}
	if got := next("?tag=adventure&q=a"); got != "/games?q=a&tag=-adventure" {
		t.Errorf("include -> exclude: %s", got)
	}
	if got := next("?tag=-adventure"); got != "/games" {
		t.Errorf("exclude -> clear: %s", got)
	}
}
