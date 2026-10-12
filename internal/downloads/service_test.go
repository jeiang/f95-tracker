package downloads

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

var ctx = context.Background()

type env struct {
	store *db.Store
	clk   *clock.Fake
	ntfy  *testutil.NtfyFake
	svc   *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := testutil.NewStore(t)
	clk := clock.NewFake(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	ntfy := testutil.NewNtfyFake(t)
	nt := notify.New(store, clk, notify.Options{URL: ntfy.URL, Topic: "t", BaseURL: "http://tracker"})
	return &env{store: store, clk: clk, ntfy: ntfy, svc: New(store, clk, nt)}
}

// game inserts a Game whose primary Source is F95 thread `thread` at `version`.
func (e *env) game(t *testing.T, name, thread, version string) sqlcgen.Game {
	t.Helper()
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: name, ExternalID: thread, LatestVersion: version, PlayStatus: domain.PlayPlaying})
	return g
}

// job returns a fresh awaiting_links job for a new Game at version.
func (e *env) job(t *testing.T, version string) Job {
	t.Helper()
	g := e.game(t, "Game "+version, "", version)
	j, created, err := e.svc.Create(ctx, g.ID)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	return j
}

// queued returns a queued job with the given supported links.
func (e *env) queued(t *testing.T, urls ...string) Job {
	t.Helper()
	j := e.job(t, "v1")
	in := make([]LinkInput, len(urls))
	for i, u := range urls {
		in[i] = LinkInput{URL: u}
	}
	res, err := e.svc.IngestLinks(ctx, j.ID, in)
	if err != nil || res.Job.State != Queued {
		t.Fatalf("ingest: %+v %v", res, err)
	}
	return res.Job
}

func (e *env) state(t *testing.T, id int64) State {
	t.Helper()
	j, err := e.svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.State
}

func (e *env) pushes() []string {
	var out []string
	for _, r := range e.ntfy.Requests() {
		out = append(out, r.Header.Get("Title"))
	}
	return out
}

func hosts(j Job) []string {
	var out []string
	for _, m := range j.Mirrors {
		out = append(out, m.Host)
	}
	return out
}

func TestOneOpenJobPerGameAndVersion(t *testing.T) {
	e := newEnv(t)
	g := e.game(t, "Alpha", "", "v1")
	first, created, err := e.svc.Create(ctx, g.ID)
	if err != nil || !created || first.State != AwaitingLinks || first.TargetVersion != "v1" || first.Trigger != TriggerButton {
		t.Fatalf("first: %+v %v %v", first, created, err)
	}
	again, created, err := e.svc.Create(ctx, g.ID)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second create must return the open job: %+v %v %v", again, created, err)
	}
	// EnsureJob (the daily check's entry) obeys the same rule, whatever the trigger.
	err = e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		j, created, err := EnsureJob(ctx, q, e.clk.Now(), NewJob{GameID: g.ID, SourceID: first.SourceID, Version: "v1", Trigger: TriggerUpdate, Actor: ActorTracker})
		if err == nil && (created || j.ID != first.ID) {
			t.Errorf("EnsureJob created a second open job: %+v", j)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A different version is a different job; a closed job no longer blocks its version.
	err = e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		j, created, err := EnsureJob(ctx, q, e.clk.Now(), NewJob{GameID: g.ID, SourceID: first.SourceID, Version: "v2", Trigger: TriggerUpdate, Actor: ActorTracker})
		if err == nil && (!created || j.Trigger != "update") {
			t.Errorf("v2: %+v %v", j, created)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Cancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	reopened, created, err := e.svc.Create(ctx, g.ID)
	if err != nil || !created || reopened.ID == first.ID {
		t.Fatalf("after cancel: %+v %v %v", reopened, created, err)
	}
	var open int
	jobs, _ := e.svc.List(ctx)
	for _, j := range jobs {
		if !j.State.Terminal() && j.TargetVersion == "v1" {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("open v1 jobs = %d", open)
	}
}

func TestCreateNeedsAKnownVersionAndGame(t *testing.T) {
	e := newEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "No version"})
	if _, _, err := e.svc.Create(ctx, g.ID); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("no version: %v", err)
	}
	if _, _, err := e.svc.Create(ctx, 9999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown game: %v", err)
	}
}

func TestIngestOrdersMirrorsByPlatformPreference(t *testing.T) {
	e := newEnv(t)
	links := []LinkInput{
		{URL: "https://gofile.io/d/lin", Label: "Linux"},
		{URL: "https://pixeldrain.com/u/win", Label: "Win"},
		{URL: "https://mega.nz/file/wl#k", Label: "Win/Linux"},
		{URL: "https://workupload.com/file/x", Label: "Linux"},
		{URL: "https://pixeldrain.com/u/lin", Label: "Linux"},
	}
	j := e.job(t, "v1")
	res, err := e.svc.IngestLinks(ctx, j.ID, links)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 5 || res.Manual != 1 || res.Job.State != Queued {
		t.Fatalf("result: %+v", res)
	}
	var got []string
	for _, m := range res.Job.Mirrors {
		got = append(got, m.Host+"/"+m.Platform+"/"+m.State)
	}
	want := []string{"pixeldrain/linux/pending", "gofile/linux/pending", "mega/win_linux/pending", "pixeldrain/win/pending", "workupload.com/linux/manual"}
	if !slices.Equal(got, want) {
		t.Fatalf("default pref (linux):\n got %v\nwant %v", got, want)
	}
	for i, m := range res.Job.Mirrors {
		if m.Position != i || m.Supported != (i < 4) {
			t.Errorf("mirror %d: %+v", i, m)
		}
	}

	// The Game's own preference overrides the default and shows as effective_platform_pref.
	g2 := e.game(t, "Windows player", "", "v1")
	if _, err := e.store.Queries().GetGame(ctx, g2.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		return q.UpdateGamePlatformPref(ctx, sqlcgen.UpdateGamePlatformPrefParams{PlatformPref: nullStr("win"), UpdatedAt: "2026-03-01T00:00:00Z", ID: g2.ID})
	}); err != nil {
		t.Fatal(err)
	}
	j2, _, err := e.svc.Create(ctx, g2.ID)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := e.svc.IngestLinks(ctx, j2.ID, links)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Job.EffectivePlatformPref != domain.PlatformWin {
		t.Errorf("effective pref %q", res2.Job.EffectivePlatformPref)
	}
	if h := hosts(res2.Job); h[0] != "pixeldrain" || res2.Job.Mirrors[0].Platform != "win" {
		t.Errorf("win pref order: %+v", res2.Job.Mirrors)
	}
}

func TestIngestOutcomes(t *testing.T) {
	e := newEnv(t)

	// No supported mirror: needs_human with the reason and exactly one push.
	j := e.job(t, "v1")
	res, err := e.svc.IngestLinks(ctx, j.ID, []LinkInput{{URL: "https://workupload.com/file/x"}})
	if err != nil || res.Job.State != NeedsHuman || res.Job.NeedsHumanReason != ReasonNoSupportedMirror || res.Accepted != 1 || res.Manual != 1 {
		t.Fatalf("no supported mirror: %+v %v", res, err)
	}
	if p := e.pushes(); len(p) != 1 || p[0] != "F95 Tracker: Download needs you" {
		t.Fatalf("pushes: %v", p)
	}
	// Pasting again while needs_human (still nothing usable) neither pushes nor changes state.
	if _, err := e.svc.IngestLinks(ctx, j.ID, []LinkInput{{URL: "https://workupload.com/file/y"}}); err != nil {
		t.Fatal(err)
	}
	if len(e.pushes()) != 1 || e.state(t, j.ID) != NeedsHuman {
		t.Fatalf("repeat paste: %v %s", e.pushes(), e.state(t, j.ID))
	}
	// Supported links on a needs_human job queue it (user action needs_human -> queued).
	res, err = e.svc.IngestLinks(ctx, j.ID, []LinkInput{{URL: "https://pixeldrain.com/u/ok"}, {URL: "https://workupload.com/file/y"}})
	if err != nil || res.Job.State != Queued || res.Job.NeedsHumanReason != "" || len(res.Job.Mirrors) != 2 {
		t.Fatalf("recovered: %+v %v", res, err)
	}
	// A queued job no longer takes links.
	if _, err := e.svc.IngestLinks(ctx, j.ID, []LinkInput{{URL: "https://pixeldrain.com/u/z"}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ingest into queued: %v", err)
	}
	if _, err := e.svc.IngestLinks(ctx, 9999, []LinkInput{{URL: "https://pixeldrain.com/u/z"}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
}

func TestMaskedLinksAreNeverStored(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, "v1")
	_, err := e.svc.IngestLinks(ctx, j.ID, []LinkInput{{URL: "https://pixeldrain.com/u/a"}, {URL: "https://f95zone.to/masked/abc/"}})
	if !errors.Is(err, ErrMasked) {
		t.Fatalf("got %v", err)
	}
	got, _ := e.svc.Get(ctx, j.ID)
	if got.State != AwaitingLinks || len(got.Mirrors) != 0 {
		t.Fatalf("job changed by a rejected submission: %+v", got)
	}
}

func TestSubmitLinksByThread(t *testing.T) {
	e := newEnv(t)
	g := e.game(t, "Threaded", "5551", "v3")
	links := []LinkInput{{URL: "https://pixeldrain.com/u/a"}}

	if _, err := e.svc.PendingForThread(ctx, "5551"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pending without job: %v", err)
	}
	if _, err := e.svc.PendingForThread(ctx, "777"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pending unknown thread: %v", err)
	}
	if _, err := e.svc.SubmitLinks(ctx, "777", links); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
	// No open job: one is created for the latest version (R-DL-5).
	res, err := e.svc.SubmitLinks(ctx, "5551", links)
	if err != nil || res.Job.GameID != g.ID || res.Job.TargetVersion != "v3" || res.Job.State != Queued || res.Job.Trigger != TriggerButton {
		t.Fatalf("submit: %+v %v", res, err)
	}
	// The job is now queued: a second submit conflicts and nothing is pending.
	if _, err := e.svc.SubmitLinks(ctx, "5551", links); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("submit to queued: %v", err)
	}
	if _, err := e.svc.PendingForThread(ctx, "5551"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pending when queued: %v", err)
	}
	// An awaiting job is found and used.
	g2 := e.game(t, "Waiting", "5552", "v1")
	j, _, _ := e.svc.Create(ctx, g2.ID)
	p, err := e.svc.PendingForThread(ctx, "5552")
	if err != nil || p.ID != j.ID {
		t.Fatalf("pending: %+v %v", p, err)
	}
	res, err = e.svc.SubmitLinks(ctx, "5552", links)
	if err != nil || res.Job.ID != j.ID {
		t.Fatalf("submit existing: %+v %v", res, err)
	}
}

func TestWorkerEventsHappyPath(t *testing.T) {
	e := newEnv(t)
	j := e.queued(t, "https://pixeldrain.com/u/a", "https://mega.nz/file/b#k")
	m1 := j.Mirrors[0].ID
	i64 := func(n int64) *int64 { return &n }

	steps := []struct {
		ev   Event
		want State
	}{
		{Event{State: Downloading, MirrorID: &m1, MirrorState: "active", BytesDone: i64(10), BytesTotal: i64(100)}, Downloading},
		{Event{State: Downloading, BytesDone: i64(60)}, Downloading}, // repeating the state is progress
		{Event{State: Extracting, MirrorID: &m1, MirrorState: "done"}, Extracting},
		{Event{State: Done, ResultPath: "/games/A/v1"}, Done},
	}
	for i, s := range steps {
		got, err := e.svc.ApplyEvent(ctx, j.ID, s.ev)
		if err != nil || got.State != s.want {
			t.Fatalf("step %d: %+v %v", i, got, err)
		}
		if i == 1 && (*got.BytesDone != 60 || *got.BytesTotal != 100 || got.ActiveMirrorID != m1) {
			t.Fatalf("progress not kept: %+v", got)
		}
	}
	got, _ := e.svc.Get(ctx, j.ID)
	if got.ResultPath != "/games/A/v1" || got.Mirrors[0].State != "done" || got.Mirrors[0].Attempts != 1 {
		t.Fatalf("final: %+v", got)
	}
	rows, _ := e.store.Queries().ListDownloadTransitions(ctx, j.ID)
	var path []string
	for _, r := range rows {
		path = append(path, r.Actor+":"+r.ToState)
	}
	if want := []string{"user:awaiting_links", "user:queued", "downloader:downloading", "downloader:extracting", "downloader:done"}; !slices.Equal(path, want) {
		t.Fatalf("audit path %v", path)
	}
	if len(e.pushes()) != 0 {
		t.Fatalf("a finished job must not push: %v", e.pushes())
	}
}

func TestMirrorFallThroughThenAllFailedNeedsHumanWithOnePush(t *testing.T) {
	e := newEnv(t)
	j := e.queued(t, "https://pixeldrain.com/u/a", "https://mega.nz/file/b#k")
	m1, m2 := j.Mirrors[0].ID, j.Mirrors[1].ID
	if _, err := e.svc.ApplyEvent(ctx, j.ID, Event{State: Downloading, MirrorID: &m1, MirrorState: "active"}); err != nil {
		t.Fatal(err)
	}
	// First mirror fails and the worker falls through: back to queued, second mirror still pending.
	got, err := e.svc.ApplyEvent(ctx, j.ID, Event{State: Queued, MirrorID: &m1, MirrorState: "failed", Error: "404"})
	if err != nil || got.State != Queued || got.Mirrors[0].State != "failed" || got.Mirrors[0].Error != "404" || got.Mirrors[1].State != "pending" {
		t.Fatalf("fall-through: %+v %v", got, err)
	}
	if _, err := e.svc.ApplyEvent(ctx, j.ID, Event{State: Downloading, MirrorID: &m2, MirrorState: "active"}); err != nil {
		t.Fatal(err)
	}
	// The last supported mirror fails: the server moves to needs_human even though the worker said queued.
	got, err = e.svc.ApplyEvent(ctx, j.ID, Event{State: Queued, MirrorID: &m2, MirrorState: "failed", Error: "quota"})
	if err != nil || got.State != NeedsHuman || got.NeedsHumanReason != ReasonAllMirrorsFailed {
		t.Fatalf("all failed: %+v %v", got, err)
	}
	if p := e.pushes(); len(p) != 1 || p[0] != "F95 Tracker: Download needs you" {
		t.Fatalf("pushes: %v", p)
	}
	body := e.ntfy.Requests()[0].Body
	if !strings.Contains(body, "Game v1") || !strings.Contains(body, ReasonAllMirrorsFailed) {
		t.Errorf("push body %q", body)
	}

	// Retry resets the failed mirrors; failing everything again pushes nothing new (once per job).
	got, err = e.svc.Retry(ctx, j.ID)
	if err != nil || got.State != Queued || got.NeedsHumanReason != "" || got.Mirrors[0].State != "pending" || got.Mirrors[1].State != "pending" {
		t.Fatalf("retry: %+v %v", got, err)
	}
	for _, m := range []int64{m1, m2} {
		m := m
		if got, err = e.svc.ApplyEvent(ctx, j.ID, Event{MirrorID: &m, MirrorState: "failed"}); err != nil {
			t.Fatal(err)
		}
	}
	if got.State != NeedsHuman || len(e.pushes()) != 1 {
		t.Fatalf("second round: %s, pushes %v", got.State, e.pushes())
	}
	// A further failure report on the needs_human job is not a second transition.
	if _, err := e.svc.ApplyEvent(ctx, j.ID, Event{MirrorID: &m2, MirrorState: "failed"}); err != nil || len(e.pushes()) != 1 {
		t.Fatalf("repeat failure: %v %v", err, e.pushes())
	}
}

func TestWorkerReportedNeedsHuman(t *testing.T) {
	e := newEnv(t)
	j := e.queued(t, "https://pixeldrain.com/u/a")
	if _, err := e.svc.ApplyEvent(ctx, j.ID, Event{State: NeedsHuman}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("needs_human without reason: %v", err)
	}
	got, err := e.svc.ApplyEvent(ctx, j.ID, Event{State: NeedsHuman, NeedsHumanReason: "archive is password protected"})
	if err != nil || got.State != NeedsHuman || got.NeedsHumanReason != "archive is password protected" {
		t.Fatalf("%+v %v", got, err)
	}
	if len(e.pushes()) != 1 {
		t.Fatalf("pushes: %v", e.pushes())
	}
}

func TestIllegalWorkerEvents(t *testing.T) {
	e := newEnv(t)
	j := e.queued(t, "https://pixeldrain.com/u/a")
	m := j.Mirrors[0].ID
	manual := e.job(t, "v2")
	if _, err := e.svc.IngestLinks(ctx, manual.ID, []LinkInput{{URL: "https://pixeldrain.com/u/a"}, {URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	manual, _ = e.svc.Get(ctx, manual.ID)

	bad := []struct {
		name string
		id   int64
		ev   Event
		is   error
	}{
		{"queued to done", j.ID, Event{State: Done}, domain.ErrIllegalTransition},
		{"queued to extracting", j.ID, Event{State: Extracting}, domain.ErrIllegalTransition},
		{"worker cannot cancel", j.ID, Event{State: Cancelled}, domain.ErrIllegalTransition},
		{"worker cannot awaiting_links", j.ID, Event{State: AwaitingLinks}, domain.ErrIllegalTransition},
		{"unknown state", j.ID, Event{State: "exploded"}, domain.ErrValidation},
		{"unknown mirror state", j.ID, Event{MirrorID: &m, MirrorState: "sulking"}, domain.ErrValidation},
		{"mirror id alone", j.ID, Event{MirrorID: &m}, domain.ErrValidation},
		{"mirror of another job", j.ID, Event{MirrorID: &manual.Mirrors[0].ID, MirrorState: "active"}, domain.ErrNotFound},
		{"manual mirror", manual.ID, Event{MirrorID: &manual.Mirrors[1].ID, MirrorState: "active"}, domain.ErrValidation},
		{"unknown job", 9999, Event{State: Downloading}, domain.ErrNotFound},
	}
	for _, c := range bad {
		if _, err := e.svc.ApplyEvent(ctx, c.id, c.ev); !errors.Is(err, c.is) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.is)
		}
	}
	if e.state(t, j.ID) != Queued {
		t.Fatalf("a rejected event changed the job: %s", e.state(t, j.ID))
	}
	// A rejected event rolls back its mirror write as well.
	got, _ := e.svc.Get(ctx, j.ID)
	if _, err := e.svc.ApplyEvent(ctx, j.ID, Event{State: Done, MirrorID: &m, MirrorState: "done"}); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatal(err)
	}
	if after, _ := e.svc.Get(ctx, j.ID); after.Mirrors[0].State != got.Mirrors[0].State {
		t.Fatalf("mirror state leaked from a rejected event: %+v", after.Mirrors[0])
	}

	// Cancelled (and done) jobs refuse everything: the worker learns to stop.
	if _, err := e.svc.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []Event{{State: Downloading}, {BytesDone: new(int64)}, {MirrorID: &m, MirrorState: "active"}} {
		if _, err := e.svc.ApplyEvent(ctx, j.ID, ev); !errors.Is(err, domain.ErrIllegalTransition) {
			t.Errorf("event on cancelled job: %v", err)
		}
	}
}

func TestUserActions(t *testing.T) {
	e := newEnv(t)
	// Cancel works from an open state, then nothing moves.
	j := e.job(t, "v1")
	if got, err := e.svc.Cancel(ctx, j.ID); err != nil || got.State != Cancelled {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	for name, f := range map[string]func(context.Context, int64) (Job, error){"cancel": e.svc.Cancel, "done": e.svc.MarkDone, "retry": e.svc.Retry} {
		if _, err := f(ctx, j.ID); !errors.Is(err, domain.ErrIllegalTransition) && !errors.Is(err, domain.ErrConflict) {
			t.Errorf("%s on cancelled: %v", name, err)
		}
	}
	// Done and retry only apply to needs_human.
	q := e.queued(t, "https://pixeldrain.com/u/a")
	if _, err := e.svc.MarkDone(ctx, q.ID); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Errorf("done on queued: %v", err)
	}
	if _, err := e.svc.Retry(ctx, q.ID); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Errorf("retry on queued: %v", err)
	}
	// Mark done on a needs_human job.
	nh := e.job(t, "v9")
	if _, err := e.svc.IngestLinks(ctx, nh.ID, []LinkInput{{URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	// Retry with no supported mirror is refused, not queued into the void.
	if _, err := e.svc.Retry(ctx, nh.ID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("retry without supported mirror: %v", err)
	}
	if got, err := e.svc.MarkDone(ctx, nh.ID); err != nil || got.State != Done {
		t.Fatalf("done: %+v %v", got, err)
	}
	// Cancelling needs_human is allowed.
	nh2 := e.job(t, "v10")
	if _, err := e.svc.IngestLinks(ctx, nh2.ID, []LinkInput{{URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Cancel(ctx, nh2.ID); err != nil || got.State != Cancelled {
		t.Fatalf("cancel needs_human: %+v %v", got, err)
	}
}

func TestWorkerJobsListsRunnableAndRecentlyCancelled(t *testing.T) {
	e := newEnv(t)
	awaiting := e.job(t, "a")
	queued := e.queued(t, "https://pixeldrain.com/u/q")
	running := e.queued(t, "https://pixeldrain.com/u/r")
	if _, err := e.svc.ApplyEvent(ctx, running.ID, Event{State: Downloading}); err != nil {
		t.Fatal(err)
	}
	nh := e.job(t, "nh")
	if _, err := e.svc.IngestLinks(ctx, nh.ID, []LinkInput{{URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	old := e.queued(t, "https://pixeldrain.com/u/old")
	if _, err := e.svc.Cancel(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(8 * 24 * time.Hour)
	recent := e.queued(t, "https://pixeldrain.com/u/recent")
	if _, err := e.svc.Cancel(ctx, recent.ID); err != nil {
		t.Fatal(err)
	}

	jobs, cancelled, err := e.svc.WorkerJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, j := range jobs {
		ids = append(ids, j.ID)
		if len(j.Mirrors) == 0 {
			t.Errorf("job %d without mirrors", j.ID)
		}
	}
	slices.Sort(ids)
	if want := []int64{queued.ID, running.ID}; !slices.Equal(ids, want) {
		t.Errorf("jobs %v, want %v (awaiting %d and needs_human %d excluded)", ids, want, awaiting.ID, nh.ID)
	}
	if want := []int64{recent.ID}; !slices.Equal(cancelled, want) {
		t.Errorf("cancelled %v, want %v (older than 7 days excluded)", cancelled, want)
	}
}
