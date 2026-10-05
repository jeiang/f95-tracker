package notify

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func lines(s string) []string { return strings.Split(s, "\n") }

func updates(n int, name string) []UpdateItem {
	var us []UpdateItem
	for i := 1; i <= n; i++ {
		us = append(us, UpdateItem{Name: fmt.Sprintf("%s %d", name, i), OldVersion: "v1", NewVersion: "v2", New: true})
	}
	return us
}

func TestBuildBodyGolden(t *testing.T) {
	cases := map[string]Digest{
		"digest_updates_only": {Updates: updates(2, "Game")},
		"digest_dev_status": {Updates: []UpdateItem{
			{Name: "Alpha", OldVersion: "v0.9", NewVersion: "v1.0", OldDevStatus: "ongoing", NewDevStatus: "completed", New: true},
			{Name: "Beta", OldVersion: "v1", NewVersion: "v2", New: true},
		}},
		"digest_all_sections": {
			Updates: updates(1, "Game"),
			NeedsYou: NeedsYou{CookieInvalid: true, TagsToReview: 3,
				Jobs: []JobLine{{JobID: 1, Text: "Game 1 v2 awaits links: /downloads/1/links", New: true}}},
			FinishedDownloads: []JobLine{{JobID: 2, Text: "Other 3 v1 downloaded"}},
			CheckFailed:       []FailedItem{{Source: "itch.io", Error: "HTTP 429 after retries", New: true}},
		},
		"digest_cap_20": {Updates: updates(15, "Up"), CheckFailed: failed(10)},
		"digest_truncate": {Updates: func() []UpdateItem {
			us := updates(20, strings.Repeat("x", 300))
			return us
		}()},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := buildBody(d)
			if len(body) > maxBodyBytes {
				t.Errorf("body is %d bytes", len(body))
			}
			testutil.Golden(t, name, lines(body))
		})
	}
}

func failed(n int) []FailedItem {
	var fs []FailedItem
	for i := 1; i <= n; i++ {
		fs = append(fs, FailedItem{Source: fmt.Sprintf("Game %d", i), Error: "parse failure", New: true})
	}
	return fs
}

func TestCoverageMatchesRenderedItems(t *testing.T) {
	d := Digest{Updates: updates(25, "Up")}
	for i := range d.Updates {
		d.Updates[i].GameID = int64(i + 1)
	}
	_, cov := buildBody(d)
	if len(cov) != maxItemLines {
		t.Errorf("covered %d items, want %d", len(cov), maxItemLines)
	}
}

type env struct {
	n     *Notifier
	fake  *testutil.NtfyFake
	store *db.Store
	clk   *clock.Fake
	raw   *sql.DB
}

func newEnv(t *testing.T) env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f95.db")
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
	fake := testutil.NewNtfyFake(t)
	clk := clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC))
	n := New(store, clk, Options{URL: fake.URL, Topic: "f95", Token: "s3cret", BaseURL: "https://f95.example/"})
	return env{n, fake, store, clk, raw}
}

func (e env) execSQL(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.raw.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestDigestRequestAndNews(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	// The review count and an already-alerted cookie never trigger a digest alone.
	sent, err := e.n.SendDigest(ctx, Digest{NeedsYou: NeedsYou{CookieInvalid: true, TagsToReview: 4}})
	if sent || err != nil || len(e.fake.Requests()) != 0 {
		t.Fatalf("sent=%v err=%v requests=%d, want nothing", sent, err, len(e.fake.Requests()))
	}
	// Old updates alone are not news.
	old := updates(1, "Old")
	old[0].New = false
	if sent, _ := e.n.SendDigest(ctx, Digest{Updates: old}); sent {
		t.Fatal("digest without new items was sent")
	}

	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Game"})
	d := Digest{Updates: updates(1, "Game"), NeedsYou: NeedsYou{TagsToReview: 4}}
	d.Updates[0].GameID = g.ID
	sent, err = e.n.SendDigest(ctx, d)
	if !sent || err != nil {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	r := e.fake.Requests()[0]
	if r.Path != "/f95" || r.Header.Get("Title") != "F95 Tracker: daily digest" || r.Header.Get("Priority") != "3" ||
		r.Header.Get("Click") != "https://f95.example/games?updates=1" || r.Header.Get("Authorization") != "Bearer s3cret" {
		t.Errorf("bad request: %+v", r)
	}
	if !strings.Contains(r.Body, "Tags to review: 4") {
		t.Errorf("review count not appended: %q", r.Body)
	}
	rows, err := e.store.Queries().ListUnsentImmediateNotifications(ctx)
	if err != nil || len(rows) != 0 {
		t.Errorf("digest must not be retryable: %v %v", rows, err)
	}
	n, err := e.store.Queries().GetNotification(ctx, 1)
	if err != nil || !n.SentAt.Valid || n.Kind != "digest" {
		t.Errorf("row = %+v err=%v", n, err)
	}
}

func TestCookieInvalidOncePerInvalidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.execSQL(t, `INSERT INTO f95_credential(id, cookie_jar, user_agent, validity, updated_at) VALUES (1,'{}','ua','valid','x')`)

	if err := e.n.CookieInvalid(ctx); err != nil || len(e.fake.Requests()) != 0 {
		t.Fatalf("pushed while cookie valid: err=%v", err)
	}
	e.execSQL(t, `UPDATE f95_credential SET validity='invalid'`)
	for range 2 {
		if err := e.n.CookieInvalid(ctx); err != nil {
			t.Fatal(err)
		}
	}
	reqs := e.fake.Requests()
	if len(reqs) != 1 || reqs[0].Header.Get("Title") != "F95 Tracker: F95 cookie invalid" || reqs[0].Header.Get("Priority") != "4" {
		t.Fatalf("requests = %+v", reqs)
	}
	// A new invalidation (alert marker cleared by the credential owner) pushes again.
	e.execSQL(t, `UPDATE f95_credential SET invalid_alerted_at=NULL`)
	if err := e.n.CookieInvalid(ctx); err != nil || len(e.fake.Requests()) != 2 {
		t.Fatalf("second invalidation: err=%v requests=%d", err, len(e.fake.Requests()))
	}
}

func TestFailedPushRecordedAndRetried(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.execSQL(t, `INSERT INTO f95_credential(id, cookie_jar, user_agent, validity, updated_at) VALUES (1,'{}','ua','invalid','x')`)
	e.fake.FailNext(1, 500)

	if err := e.n.CookieInvalid(ctx); err == nil {
		t.Fatal("want delivery error")
	}
	row, _ := e.store.Queries().GetNotification(ctx, 1)
	if row.SentAt.Valid || !row.Error.Valid || !strings.Contains(row.Error.String, "500") {
		t.Fatalf("row = %+v", row)
	}
	// Not re-pushed by a second call, only by the retry.
	if err := e.n.CookieInvalid(ctx); err != nil || len(e.fake.Requests()) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(e.fake.Requests()))
	}
	sent, err := e.n.RetryUnsent(ctx)
	if sent != 1 || err != nil {
		t.Fatalf("retry sent=%d err=%v", sent, err)
	}
	row, _ = e.store.Queries().GetNotification(ctx, 1)
	if !row.SentAt.Valid || row.Error.Valid {
		t.Fatalf("row after retry = %+v", row)
	}
	if sent, _ := e.n.RetryUnsent(ctx); sent != 0 || len(e.fake.Requests()) != 2 {
		t.Fatalf("resent a delivered push")
	}
}

func TestFailedDigestNotRetried(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.fake.FailNext(1, 503)
	sent, err := e.n.SendDigest(ctx, Digest{Updates: updates(1, "G")})
	if sent || err == nil {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if n, _ := e.n.RetryUnsent(ctx); n != 0 || len(e.fake.Requests()) != 1 {
		t.Fatal("digest was retried")
	}
	row, _ := e.store.Queries().GetNotification(ctx, 1)
	if row.SentAt.Valid || !row.Error.Valid {
		t.Fatalf("row = %+v", row)
	}
}

func TestSourceUnavailableOncePerTransition(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, src := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Gone"})
	e.execSQL(t, `UPDATE source SET unavailable_at='2026-10-05T08:00:00Z' WHERE id=?`, src.ID)
	for range 2 {
		if err := e.n.SourceUnavailable(ctx, src.ID, "Gone"); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.fake.Requests()) != 1 || e.fake.Requests()[0].Header.Get("Title") != "F95 Tracker: Source unavailable" {
		t.Fatalf("requests = %+v", e.fake.Requests())
	}
	// Re-enabled, then unavailable again later: a new transition pushes again.
	e.clk.Advance(48 * time.Hour)
	e.execSQL(t, `UPDATE source SET unavailable_at='2026-10-07T08:00:00Z' WHERE id=?`, src.ID)
	if err := e.n.SourceUnavailable(ctx, src.ID, "Gone"); err != nil || len(e.fake.Requests()) != 2 {
		t.Fatalf("err=%v requests=%d", err, len(e.fake.Requests()))
	}
}

func TestTestPush(t *testing.T) {
	e := newEnv(t)
	if err := e.n.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := e.fake.Requests()[0]
	if r.Header.Get("Title") != "F95 Tracker: test" || r.Header.Get("Priority") != "3" {
		t.Errorf("headers = %v", r.Header)
	}
}
