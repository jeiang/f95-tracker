package check

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type dailyEnv struct {
	*env
	ntfy   *testutil.NtfyFake
	runner *Runner
}

// newDaily wires a Runner in check mode: the retry ladder and itch.io pacing
// sleep on the fake clock instead of the wall clock.
func newDaily(t *testing.T, withCookie bool) *dailyEnv {
	t.Helper()
	e := newEnv(t, withCookie)
	sleep := func(_ context.Context, d time.Duration) error { e.clk.Advance(d); return nil }
	fc, err := f95.New(f95.Options{BaseURL: e.fake.URL(), Creds: e.creds, Version: "test", Retry: f95.DefaultRetry, Sleep: sleep})
	if err != nil {
		t.Fatal(err)
	}
	ic := itch.NewClient(itch.Options{Clock: e.clk, BaseURL: e.ifake.URL, Sleep: sleep})
	gs := games.New(e.store, e.clk, games.Options{StateDir: e.state, HTTPClient: &http.Client{Transport: e.cover}})
	ntfy := testutil.NewNtfyFake(t)
	nt := notify.New(e.store, e.clk, notify.Options{URL: ntfy.URL, Topic: "t", BaseURL: "http://tracker"})
	r := NewRunner(Deps{
		Store: e.store, Clock: e.clk, Tags: e.tagsvc, Notify: nt, F95: fc, Creds: e.creds, Itch: ic,
		Refresher: NewRefresher(e.store, e.clk, gs, e.tagsvc, fc, ic, nil),
		LockPath:  e.state + "/check.lock", Local: time.UTC,
	})
	return &dailyEnv{env: e, ntfy: ntfy, runner: r}
}

func (d *dailyEnv) run(o Options) {
	d.t.Helper()
	if err := d.runner.Run(ctx, o); err != nil {
		d.t.Fatal(err)
	}
}

func (d *dailyEnv) nextDay() { d.clk.Advance(24 * time.Hour) }

// titles lists the Title header of every push received, in order.
func (d *dailyEnv) titles() []string {
	var out []string
	for _, r := range d.ntfy.Requests() {
		out = append(out, r.Header.Get("Title"))
	}
	return out
}

func (d *dailyEnv) lastBody() string {
	rs := d.ntfy.Requests()
	if len(rs) == 0 {
		d.t.Fatal("no push received")
	}
	return rs[len(rs)-1].Body
}

func (d *dailyEnv) latestRun() sqlcgen.CheckRun {
	run, err := d.store.Queries().GetLatestCheckRun(ctx)
	if err != nil {
		d.t.Fatal(err)
	}
	return run
}

func (d *dailyEnv) count(query string, args ...any) int {
	d.t.Helper()
	var n int
	if err := d.raw.QueryRow(query, args...).Scan(&n); err != nil {
		d.t.Fatal(err)
	}
	return n
}

func (d *dailyEnv) f95Game(name string, status domain.PlayStatus, thread, version string, at time.Time) sqlcgen.Source {
	_, s := testutil.InsertGame(d.t, d.store, testutil.GameSpec{
		Name: name, PlayStatus: status, ExternalID: thread, LatestVersion: version, DevStatus: domain.DevCompleted, At: at,
	})
	return s
}

func (d *dailyEnv) serveThread(t *testing.T, ids ...string) {
	for _, id := range ids {
		d.fake.SetThread(id, fixture(t, "67494.html"), fixture(t, "59416.guest.html"))
	}
}

var oldDay = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestUpdateDigestAlertVersusNonAlert(t *testing.T) {
	d := newDaily(t, true)
	d.serveThread(t, "67494", "67426", "114650")
	d.f95Game("Alpha", domain.PlayPlaying, "67494", "v4.0", oldDay)
	b := d.f95Game("Bravo", domain.PlayFinished, "67426", "v1", oldDay.Add(time.Hour))
	c := d.f95Game("Charlie", domain.PlayPlaying, "114650", "v9", oldDay.Add(2*time.Hour))
	d.fake.SetVersion("67494", "v4.34.1 Public")
	d.fake.SetVersion("67426", "v2")
	d.fake.SetVersion("114650", "v9")

	d.run(Options{})

	body := d.lastBody()
	if !strings.Contains(body, "Alpha: v4.0 → v4.34.1 Public (Dev status: completed → ongoing)") {
		t.Errorf("alert-set Update missing from digest:\n%s", body)
	}
	if strings.Contains(body, "Bravo") || strings.Contains(body, "Charlie") {
		t.Errorf("non-alert and unchanged Games must not be listed:\n%s", body)
	}
	if got := d.titles(); len(got) != 1 || got[0] != "F95 Tracker: daily digest" {
		t.Errorf("pushes = %v", got)
	}
	// The non-alert Game still records the new key, so a later status change does not alert on it.
	if s := d.source(b.ID); s.ChangeKey.String != "v2" || s.LatestVersion.String != "v2" {
		t.Errorf("non-alert source = %+v", s)
	}
	if rs := d.results(b.ID); rs[0].Outcome != OutcomeUpdate || rs[0].OldKey.String != "v1" || rs[0].NewKey.String != "v2" {
		t.Errorf("non-alert results = %+v", rs)
	}
	if rs := d.results(c.ID); rs[0].Outcome != OutcomeUnchanged {
		t.Errorf("unchanged results = %+v", rs)
	}
	if run := d.latestRun(); run.Kind != "daily" || run.Status != "ok" || run.F95Stopped != 0 {
		t.Errorf("run = %+v", run)
	}
}

func TestUnchangedKeysSendNoDigest(t *testing.T) {
	d := newDaily(t, false)
	s := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v4.0", oldDay)
	d.fake.SetVersion("67494", "v4.0")
	d.run(Options{})
	if got := d.titles(); len(got) != 0 {
		t.Errorf("pushes = %v", got)
	}
	if got := d.source(s.ID); !got.LastCheckedAt.Valid || got.MissCount != 0 {
		t.Errorf("source = %+v", got)
	}
	// Nothing needs a detail fetch without a stored cookie: no thread page was requested.
	for _, r := range d.fake.Requests() {
		if strings.HasPrefix(r.Path, "/threads/") {
			t.Errorf("thread request without credential: %s", r.Path)
		}
	}
}

func (d *dailyEnv) unavailablePushes() int {
	n := 0
	for _, title := range d.titles() {
		if title == "F95 Tracker: Source unavailable" {
			n++
		}
	}
	return n
}

func (d *dailyEnv) recentDetail(id int64) {
	d.exec(`UPDATE source SET last_detail_at = '2026-10-04T00:00:00Z' WHERE id = ?`, id)
}

func TestThreeMissesThenConfirmingFetchMarksUnavailableOnce(t *testing.T) {
	d := newDaily(t, true)
	d.serveThread(t, "67426") // the cookie probe thread
	d.f95Game("Probe", domain.PlayFinished, "67426", "v1", oldDay)
	d.fake.SetVersion("67426", "v1")
	gone := d.f95Game("Gone", domain.PlayPlaying, "67494", "v4.0", oldDay.Add(time.Hour))
	d.recentDetail(gone.ID)
	d.serveThread(t, "67494")
	d.fake.Program("67494", testutil.F95Repeat(testutil.F95Restricted(), 3)...)

	for i, want := range []int64{1, 2} {
		d.run(Options{})
		if s := d.source(gone.ID); s.MissCount != want || s.UnavailableAt.Valid {
			t.Fatalf("run %d: source = %+v", i+1, s)
		}
		d.nextDay()
	}
	d.run(Options{})
	s := d.source(gone.ID)
	if !s.UnavailableAt.Valid || !s.UnavailableReason.Valid || s.MissCount != 3 {
		t.Fatalf("after 3 misses: %+v", s)
	}
	if n := d.unavailablePushes(); n != 1 {
		t.Errorf("pushes after unavailable = %v", d.titles())
	}
	// Excluded from checks from now on: not asked again, no second push.
	d.nextDay()
	d.run(Options{})
	if n := d.unavailablePushes(); n != 1 {
		t.Errorf("second push: %v", d.titles())
	}
	last := d.fake.Requests()
	for _, r := range last[len(last)-3:] {
		if strings.Contains(r.Query, "67494") {
			t.Errorf("unavailable Source still checked: %+v", r)
		}
	}
}

func TestConfirmingFetchThatLoadsResetsMisses(t *testing.T) {
	d := newDaily(t, true)
	d.serveThread(t, "67494", "67426")
	d.f95Game("Probe", domain.PlayFinished, "67426", "v1", oldDay)
	d.fake.SetVersion("67426", "v1")
	s := d.f95Game("Quiet", domain.PlayPlaying, "67494", "v4.0", oldDay.Add(time.Hour))
	d.recentDetail(s.ID)
	for range 3 {
		d.run(Options{})
		d.nextDay()
	}
	got := d.source(s.ID)
	if got.MissCount != 0 || got.UnavailableAt.Valid {
		t.Errorf("source = %+v", got)
	}
	if n := d.unavailablePushes(); n != 0 {
		t.Errorf("pushes = %v", d.titles())
	}
	if !got.LastDetailAt.Valid || got.LastDetailAt.String == "2026-10-04T00:00:00Z" {
		t.Errorf("no confirming detail fetch happened: %+v", got)
	}
}

func TestBlockedCheckerStopsAllF95Requests(t *testing.T) {
	d := newDaily(t, true)
	d.serveThread(t, "67494")
	s := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v1", oldDay)
	d.fake.SetVersion("67494", "v2")
	d.fake.Program("checker", testutil.F95Repeat(testutil.F95Status(429), 4)...)
	start := d.clk.Now()

	d.run(Options{})

	if run := d.latestRun(); run.F95Stopped != 1 || run.Status != "failed" {
		t.Errorf("run = %+v", run)
	}
	if got := d.clk.Now().Sub(start); got != 21*time.Minute {
		t.Errorf("ladder slept %v, want 1+5+15 min", got)
	}
	if reqs := d.fake.Requests(); len(reqs) != 4 {
		t.Errorf("requests after the block = %d, want only the 4 checker attempts", len(reqs))
	}
	if body := d.lastBody(); !strings.Contains(body, "Check failed") || !strings.Contains(body, "F95 checker") {
		t.Errorf("digest:\n%s", body)
	}
	if got := d.source(s.ID); got.ChangeKey.String != "v1" {
		t.Errorf("data changed: %+v", got)
	}
}

func TestCheckerParseFailureLeavesDataUntouched(t *testing.T) {
	d := newDaily(t, false)
	s := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v1", oldDay)
	d.fake.SetVersion("67494", "v2")
	d.fake.Program("checker", testutil.F95Response{Body: `{"status":"error","msg":"nope"}`})

	d.run(Options{})

	rs := d.results(s.ID)
	if len(rs) != 1 || rs[0].Outcome != OutcomeError || rs[0].Step != StepChecker {
		t.Errorf("results = %+v", rs)
	}
	got := d.source(s.ID)
	if got.ChangeKey.String != "v1" || got.LatestVersion.String != "v1" || got.MissCount != 0 || got.LastCheckedAt.Valid {
		t.Errorf("data touched: %+v", got)
	}
	if run := d.latestRun(); run.F95Stopped != 0 {
		t.Errorf("a parse failure is not a block: %+v", run)
	}
	if body := d.lastBody(); !strings.Contains(body, "Check failed") {
		t.Errorf("digest:\n%s", body)
	}
	if d.count(`SELECT COUNT(*) FROM detail_fetch_queue WHERE reason = 'update'`) != 0 {
		t.Error("an Update was enqueued from a failed answer")
	}
}

func TestCookieInvalidPushesOnceAndKeepsQueue(t *testing.T) {
	d := newDaily(t, true)
	d.fake.SetValidUser("someone-else") // the stored cookie now gets the logged-out page
	d.serveThread(t, "67494")
	s := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v1", oldDay)
	d.recentDetail(s.ID)
	d.fake.SetVersion("67494", "v2")

	d.run(Options{})
	d.nextDay()
	d.run(Options{})

	cookiePushes := 0
	for _, title := range d.titles() {
		if title == "F95 Tracker: F95 cookie invalid" {
			cookiePushes++
		}
	}
	if cookiePushes != 1 {
		t.Errorf("cookie pushes = %d in %v", cookiePushes, d.titles())
	}
	if !d.queued(s.ID) {
		t.Error("queue must stay intact while the cookie is invalid")
	}
	if got := d.source(s.ID); got.LastDetailAt.String != "2026-10-04T00:00:00Z" {
		t.Errorf("a detail fetch ran with an invalid cookie: %+v", got)
	}
	cred, _ := d.creds.Get(ctx)
	if cred.Validity != "invalid" {
		t.Errorf("validity = %q", cred.Validity)
	}
	// The first run's digest (Update) carries the Needs you line.
	first := d.ntfy.Requests()
	var digest string
	for _, r := range first {
		if r.Header.Get("Title") == "F95 Tracker: daily digest" {
			digest = r.Body
		}
	}
	if !strings.Contains(digest, "F95 cookie is invalid") || !strings.Contains(digest, "Alpha: v1 → v2") {
		t.Errorf("digest:\n%s", digest)
	}
}

func TestDailyDetailCap(t *testing.T) {
	d := newDaily(t, true)
	const n = 45
	for i := range n {
		id := fmt.Sprint(2000 + i)
		d.serveThread(t, id)
		s := d.f95Game("G"+id, domain.PlayPlanned, id, "v1", oldDay.Add(time.Duration(i)*time.Minute))
		d.fake.SetVersion(id, "v1")
		d.recentDetail(s.ID)
		d.enqueue(s.ID)
	}
	details := func() int { return d.count(`SELECT COUNT(*) FROM check_result WHERE step = 'detail'`) }
	queue := func() int { return d.count(`SELECT COUNT(*) FROM detail_fetch_queue`) }

	d.run(Options{})
	if details() != 40 || queue() != 5 {
		t.Fatalf("day 1: %d fetches, %d queued", details(), queue())
	}
	d.clk.Advance(time.Hour)
	d.run(Options{})
	if details() != 40 || queue() != 5 {
		t.Fatalf("day 1 again: %d fetches, %d queued", details(), queue())
	}
	d.nextDay()
	d.run(Options{})
	if details() != 45 || queue() != 0 {
		t.Fatalf("day 2: %d fetches, %d queued", details(), queue())
	}
}

func TestWeeklyEnqueueLimits(t *testing.T) {
	d := newDaily(t, false) // no cookie: the queue is filled but not drained
	var old []int64
	for i := range 25 {
		id := fmt.Sprint(3000 + i)
		s := d.f95Game("G"+id, domain.PlayPlaying, id, "v1", oldDay)
		d.fake.SetVersion(id, "v1")
		d.exec(`UPDATE source SET last_detail_at = ? WHERE id = ?`, fmt.Sprintf("2026-09-01T00:%02d:00Z", i), s.ID)
		old = append(old, s.ID)
	}
	never := d.f95Game("Never", domain.PlayPlaying, "3100", "v1", oldDay)
	d.fake.SetVersion("3100", "v1")
	finished := d.f95Game("Done", domain.PlayFinished, "3101", "v1", oldDay)
	d.fake.SetVersion("3101", "v1")
	d.exec(`UPDATE source SET last_detail_at = '2026-09-01T00:00:00Z' WHERE id = ?`, finished.ID)
	recent := d.f95Game("Recent", domain.PlayPlaying, "3102", "v1", oldDay)
	d.fake.SetVersion("3102", "v1")
	d.exec(`UPDATE source SET last_detail_at = '2026-10-04T08:00:00Z' WHERE id = ?`, recent.ID)

	d.run(Options{OnlyChecker: true})
	if n := d.count(`SELECT COUNT(*) FROM detail_fetch_queue`); n != 0 {
		t.Fatalf("--only-checker enqueued %d", n)
	}
	d.run(Options{})

	if n := d.count(`SELECT COUNT(*) FROM detail_fetch_queue WHERE reason = 'weekly' AND budget = 'routine'`); n != 20 {
		t.Fatalf("weekly queue = %d, want 20", n)
	}
	// Never fetched first, then oldest first; finished and recent Games never.
	if !d.queued(never.ID) || !d.queued(old[0]) || !d.queued(old[18]) {
		t.Error("oldest Sources missing from the queue")
	}
	if d.queued(old[19]) || d.queued(old[24]) || d.queued(finished.ID) || d.queued(recent.ID) {
		t.Error("queue holds a Source it must not")
	}
}

func TestItchUpdateAndDevlogOnDigestLine(t *testing.T) {
	d := newDaily(t, false)
	page, _ := os.ReadFile("../itch/testdata/s1.html")
	devlog, _ := os.ReadFile("../itch/testdata/s2.devlog.rss")
	d.ifake.Set("/lycoris-radiata", page)
	d.ifake.Set("/lycoris-radiata/devlog.rss", devlog)
	_, alerting := testutil.InsertGame(t, d.store, testutil.GameSpec{Name: "Lycoris", PlayStatus: domain.PlayPlaying, Kind: domain.SourceItchio, URL: "https://kuro.itch.io/lycoris-radiata", ExternalID: "lycoris", ChangeKey: "updated=old", LatestVersion: "old"})

	d.run(Options{})

	body := d.lastBody()
	if !strings.Contains(body, "Lycoris: old → Ch7 (devlog: The EP33 has just been released!)") {
		t.Errorf("digest:\n%s", body)
	}
	if got := d.source(alerting.ID); got.LatestVersion.String != "Ch7" || got.MissCount != 0 {
		t.Errorf("source = %+v", got)
	}
	// Same page tomorrow: unchanged, no digest.
	d.nextDay()
	before := len(d.ntfy.Requests())
	d.run(Options{})
	if len(d.ntfy.Requests()) != before {
		t.Errorf("unchanged itch.io page pushed: %v", d.titles())
	}
}

func TestItchNotFoundCountsMissesAndMarksUnavailable(t *testing.T) {
	d := newDaily(t, false)
	_, s := testutil.InsertGame(t, d.store, testutil.GameSpec{Name: "Gone", Kind: domain.SourceItchio, URL: "https://a.itch.io/gone", ExternalID: "gone", ChangeKey: "k"})
	for i, want := range []int64{1, 2} {
		d.run(Options{})
		if got := d.source(s.ID); got.MissCount != want || got.UnavailableAt.Valid {
			t.Fatalf("run %d: %+v", i+1, got)
		}
	}
	d.run(Options{})
	if got := d.source(s.ID); !got.UnavailableAt.Valid {
		t.Fatalf("not unavailable after 3 misses: %+v", got)
	}
	if n := d.unavailablePushes(); n != 1 {
		t.Errorf("pushes = %v", d.titles())
	}
}

func TestItchRateLimitStopsItchAndReportsCheckFailed(t *testing.T) {
	d := newDaily(t, false)
	testutil.InsertGame(t, d.store, testutil.GameSpec{Name: "One", Kind: domain.SourceItchio, URL: "https://a.itch.io/one", ExternalID: "one", ChangeKey: "k"})
	testutil.InsertGame(t, d.store, testutil.GameSpec{Name: "Two", Kind: domain.SourceItchio, URL: "https://a.itch.io/two", ExternalID: "two", ChangeKey: "k"})
	d.ifake.Respond("/one", 429, 429, 429, 429)

	d.run(Options{})

	if reqs := d.ifake.Requests(); len(reqs) != 4 {
		t.Errorf("itch.io requests = %d, want the 4 attempts on the first page only", len(reqs))
	}
	if body := d.lastBody(); !strings.Contains(body, "Check failed") || !strings.Contains(body, "itch.io") {
		t.Errorf("digest:\n%s", body)
	}
	if run := d.latestRun(); run.F95Stopped != 0 {
		t.Errorf("itch.io block must not set f95_stopped: %+v", run)
	}
}

func TestConcurrentRunHoldsLock(t *testing.T) {
	d := newDaily(t, false)
	f, err := os.OpenFile(d.runner.LockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	d.run(Options{}) // exits cleanly without doing anything
	if n := d.count(`SELECT COUNT(*) FROM check_run`); n != 0 {
		t.Fatalf("a locked-out run created %d check_run rows", n)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	d.run(Options{})
	if n := d.count(`SELECT COUNT(*) FROM check_run WHERE kind = 'daily' AND status = 'ok'`); n != 1 {
		t.Fatalf("check_run rows = %d", n)
	}
}

func TestNoDigestStillWritesData(t *testing.T) {
	d := newDaily(t, false)
	s := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v1", oldDay)
	d.fake.SetVersion("67494", "v2")
	d.run(Options{NoDigest: true})
	if got := d.source(s.ID); got.ChangeKey.String != "v2" {
		t.Errorf("source = %+v", got)
	}
	if len(d.ntfy.Requests()) != 0 {
		t.Errorf("pushes = %v", d.titles())
	}
}

func TestOnlyCheckerSkipsItchAndDetails(t *testing.T) {
	d := newDaily(t, true)
	testutil.InsertGame(t, d.store, testutil.GameSpec{Name: "Itch", Kind: domain.SourceItchio, URL: "https://a.itch.io/one", ExternalID: "one", ChangeKey: "k"})
	d.f95Game("Alpha", domain.PlayPlaying, "67494", "v1", oldDay)
	d.fake.SetVersion("67494", "v2")
	d.run(Options{OnlyChecker: true})
	if len(d.ifake.Requests()) != 0 {
		t.Error("itch.io was requested")
	}
	for _, r := range d.fake.Requests() {
		if strings.HasPrefix(r.Path, "/threads/") {
			t.Errorf("thread request %s", r.Path)
		}
	}
	if body := d.lastBody(); !strings.Contains(body, "Alpha: v1 → v2") {
		t.Errorf("digest:\n%s", body)
	}
}
