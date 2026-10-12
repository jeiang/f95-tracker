package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/worker/api"
)

type fakeTracker struct {
	mu        sync.Mutex
	jobs      []api.Job
	cancelled []int64
	events    []api.Event
	state     string
	auth      string
}

func (f *fakeTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(api.JobsResponse{Jobs: f.jobs, Cancelled: f.cancelled})
		return
	}
	var ev api.Event
	json.NewDecoder(r.Body).Decode(&ev)
	f.events = append(f.events, ev)
	switch {
	case ev.State != "":
		f.state = ev.State
	case ev.MirrorState == "failed": // last mirror failed: tracker decides
		f.state = "needs_human"
	}
	job := f.jobs[0]
	job.State = f.state
	json.NewEncoder(w).Encode(map[string]any{"job": job})
}

func (f *fakeTracker) snapshot() []api.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.Event(nil), f.events...)
}

// seq renders the non-progress events as "state[/mirror:mirror_state]".
func seq(evs []api.Event) []string {
	var out []string
	for _, e := range evs {
		if e.State == "" && e.MirrorState == "" {
			continue
		}
		s := e.State
		if e.MirrorState != "" {
			s += "/" + strconv.FormatInt(*e.MirrorID, 10) + ":" + e.MirrorState
		}
		out = append(out, s)
	}
	return out
}

type fakePixeldrain struct {
	mu     sync.Mutex
	files  map[string]fakeFile
	ranges []string
	block  chan struct{} // when set, downloads hang until the request is cancelled
}

type fakeFile struct {
	name string
	data []byte
}

func (p *fakePixeldrain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/file/")
	id, info := strings.CutSuffix(rest, "/info")
	f, ok := p.files[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if info {
		json.NewEncoder(w).Encode(map[string]any{"name": f.name, "size": len(f.data)})
		return
	}
	p.mu.Lock()
	p.ranges = append(p.ranges, r.Header.Get("Range"))
	p.mu.Unlock()
	if p.block != nil {
		w.Write(f.data[:1])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
}

type env struct {
	t       *testing.T
	games   string
	state   string
	tracker *fakeTracker
	pd      *fakePixeldrain
	worker  *Worker
}

func script(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T, job api.Job) *env {
	t.Helper()
	e := &env{t: t, games: t.TempDir(), state: t.TempDir()}
	bin := t.TempDir()
	// Fakes: the extractor writes game.exe into the -o dir; mega-get fails for
	// URLs containing "bad" else writes a zip; gallery-dl does the same for -D.
	script(t, bin, "7zz", `for a; do case "$a" in -o*) d="${a#-o}";; esac; done
mkdir -p "$d" && echo extracted > "$d/game.exe"`)
	script(t, bin, "unar", `while [ $# -gt 0 ]; do [ "$1" = -output-directory ] && d="$2"; shift; done
mkdir -p "$d" && echo rar > "$d/game.exe"`)
	script(t, bin, "mega-get", `case "$1" in *bad*) echo "quota" >&2; exit 1;; esac
echo zip > "$2/game.zip"`)
	script(t, bin, "gallery-dl", `case "$3" in *bad*) exit 1;; esac
echo zip > "$2/game.zip"`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	job.State = "queued"
	e.tracker = &fakeTracker{jobs: []api.Job{job}, state: "queued"}
	ts := httptest.NewServer(e.tracker)
	t.Cleanup(ts.Close)
	e.pd = &fakePixeldrain{files: map[string]fakeFile{"abc": {"Game-1.0.zip", bytes.Repeat([]byte("0123456789"), 1000)}}}
	ps := httptest.NewServer(e.pd)
	t.Cleanup(ps.Close)
	hosts := DefaultHosts(e.state, ps.URL)
	hosts["pixeldrain"].(*Pixeldrain).RetryDelay = time.Millisecond
	w, err := New(Options{
		Client:   &api.Client{BaseURL: ts.URL, Token: "f95t_x"},
		GamesDir: e.games, PollInterval: 20 * time.Millisecond, Hosts: hosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.worker = w
	return e
}

func mirror(id int64, pos int, host, url string, supported bool) api.Mirror {
	return api.Mirror{ID: id, Position: pos, Host: host, URL: url, Supported: supported, State: "pending"}
}

func mkJob(game, version string, ms ...api.Mirror) api.Job {
	j := api.Job{ID: 7, TargetVersion: version, Mirrors: ms}
	j.Game.ID, j.Game.Name = 1, game
	return j
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events\n got %v\nwant %v", got, want)
	}
}

func TestPixeldrainHappyPath(t *testing.T) {
	e := newEnv(t, mkJob("Cool/Game", ".v1.0", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/u/abc", true)))
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, seq(e.tracker.snapshot()), []string{"downloading/10:active", "extracting", "done/10:done"})
	dest := filepath.Join(e.games, "Cool_Game", "_v1.0")
	evs := e.tracker.snapshot()
	if got := evs[len(evs)-1].ResultPath; got != dest {
		t.Fatalf("result_path %q, want %q", got, dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "game.exe")); err != nil {
		t.Fatal("not extracted:", err)
	}
	if left, _ := os.ReadDir(filepath.Join(e.games, ".incoming")); len(left) != 0 {
		t.Fatalf("archive not deleted: %v", left)
	}
	if e.tracker.auth != "Bearer f95t_x" {
		t.Fatalf("auth header %q", e.tracker.auth)
	}
}

func TestPixeldrainResumesWithRange(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/u/abc", true)))
	data := e.pd.files["abc"].data
	part := filepath.Join(e.games, ".incoming", "7-10.part")
	if err := os.WriteFile(part, data[:4000], 0o644); err != nil {
		t.Fatal(err)
	}
	var got []byte
	// Capture the assembled file just before extraction by wrapping the extractor.
	bin := filepath.Dir(mustLook(t, "7zz"))
	script(t, bin, "7zz", `for a; do case "$a" in -o*) d="${a#-o}";; esac; last="$a"; done
mkdir -p "$d" && cp "$last" "$d/assembled.zip"`)
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.pd.ranges) != 1 || e.pd.ranges[0] != "bytes=4000-" {
		t.Fatalf("ranges %v", e.pd.ranges)
	}
	got, _ = os.ReadFile(filepath.Join(e.games, "G", "1", "assembled.zip"))
	if !bytes.Equal(got, data) {
		t.Fatalf("assembled file differs (%d bytes, want %d)", len(got), len(data))
	}
}

func mustLook(t *testing.T, name string) string {
	t.Helper()
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if p := filepath.Join(d, name); fileExists(p) {
			return p
		}
	}
	t.Fatal("missing", name)
	return ""
}

func TestMirrorFallThrough(t *testing.T) {
	e := newEnv(t, mkJob("G", "1",
		mirror(12, 2, "pixeldrain", "https://pixeldrain.com/u/abc", true),
		mirror(11, 1, "buzzheavier", "https://buzzheavier.com/x", false),
		mirror(10, 0, "mega", "https://mega.nz/file/bad#k", true),
	))
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := e.tracker.snapshot()
	eq(t, seq(evs), []string{
		"downloading/10:active", "queued/10:failed",
		"downloading/12:active", "extracting", "done/12:done",
	})
	if !strings.Contains(evs[1].Error, "quota") {
		t.Fatalf("failure error not reported: %q", evs[1].Error)
	}
}

func TestAllMirrorsFailedReportsLastFailureWithoutState(t *testing.T) {
	e := newEnv(t, mkJob("G", "1",
		mirror(10, 0, "mega", "https://mega.nz/file/bad#k", true),
		mirror(11, 1, "gofile", "https://gofile.io/d/bad", true),
	))
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, seq(e.tracker.snapshot()), []string{
		"downloading/10:active", "queued/10:failed",
		"downloading/11:active", "/11:failed",
	})
	if e.tracker.state != "needs_human" {
		t.Fatalf("tracker state %q", e.tracker.state)
	}
}

func TestExtractionFailureFallsThrough(t *testing.T) {
	e := newEnv(t, mkJob("G", "1",
		mirror(10, 0, "gofile", "https://gofile.io/d/ok", true),
		mirror(11, 1, "pixeldrain", "https://pixeldrain.com/u/abc", true),
	))
	script(t, filepath.Dir(mustLook(t, "7zz")), "7zz", `
for a; do case "$a" in *game.zip) echo corrupt >&2; exit 2;; esac; done
for a; do case "$a" in -o*) d="${a#-o}";; esac; done
mkdir -p "$d" && echo ok > "$d/game.exe"`)
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, seq(e.tracker.snapshot()), []string{
		"downloading/10:active", "extracting", "queued/10:failed",
		"downloading/11:active", "extracting", "done/11:done",
	})
	if _, err := os.Stat(filepath.Join(e.games, "G", "1", "game.exe")); err != nil {
		t.Fatal(err)
	}
}

func TestNonArchiveMovedAsIs(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/u/abc", true)))
	e.pd.files["abc"] = fakeFile{"setup.exe", []byte("MZ")}
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.games, "G", "1", "setup.exe"))
	if err != nil || string(b) != "MZ" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestPixeldrainListLinkIsAFailure(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/l/xyz", true)))
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := e.tracker.snapshot()
	eq(t, seq(evs), []string{"downloading/10:active", "/10:failed"})
	if !strings.Contains(evs[1].Error, "list") {
		t.Fatalf("error %q", evs[1].Error)
	}
}

func TestCancellationStopsLocalWork(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/u/abc", true)))
	e.pd.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- e.worker.RunOnce(context.Background()) }()
	// Wait for the download to start, then cancel on the tracker.
	deadline := time.Now().Add(5 * time.Second)
	for len(e.tracker.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("download never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.tracker.mu.Lock()
	e.tracker.jobs, e.tracker.cancelled = nil, []int64{7}
	e.tracker.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	eq(t, seq(e.tracker.snapshot()), []string{"downloading/10:active"})
	if left, _ := os.ReadDir(filepath.Join(e.games, ".incoming")); len(left) != 0 {
		t.Fatalf("partial files left: %v", left)
	}
}

func TestShutdownKeepsPartialForResume(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/u/abc", true)))
	e.pd.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.worker.RunOnce(ctx) }()
	for len(e.tracker.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if _, err := os.Stat(filepath.Join(e.games, ".incoming", "7-10.part")); err != nil {
		t.Fatal("partial file should survive shutdown:", err)
	}
	eq(t, seq(e.tracker.snapshot()), []string{"downloading/10:active"})
}

func TestUserDroppedArchive(t *testing.T) {
	for name, drop := range map[string]func(inc string) error{
		"dir": func(inc string) error {
			os.MkdirAll(filepath.Join(inc, "7"), 0o755)
			return os.WriteFile(filepath.Join(inc, "7", "g.7z"), []byte("x"), 0o644)
		},
		"file": func(inc string) error { return os.WriteFile(filepath.Join(inc, "7.zip"), []byte("x"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, mkJob("G", "2", mirror(10, 0, "mega", "https://mega.nz/file/bad#k", true)))
			if err := drop(filepath.Join(e.games, ".incoming")); err != nil {
				t.Fatal(err)
			}
			if err := e.worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			eq(t, seq(e.tracker.snapshot()), []string{"downloading", "extracting", "done"})
			if _, err := os.Stat(filepath.Join(e.games, "G", "2", "game.exe")); err != nil {
				t.Fatal(err)
			}
			if left, _ := os.ReadDir(filepath.Join(e.games, ".incoming")); len(left) != 0 {
				t.Fatalf("dropped archive not removed: %v", left)
			}
		})
	}
}

func TestNoSupportedMirror(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "bowfile.com", "https://bowfile.com/x", false)))
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := e.tracker.snapshot()
	if len(evs) != 1 || evs[0].State != "needs_human" || evs[0].NeedsHumanReason == "" {
		t.Fatalf("events %+v", evs)
	}
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"Plain Name":   "Plain Name",
		"a/b":          "a_b",
		"../x":         "___x",
		".hidden":      "_hidden",
		"v1.0 [Final]": "v1.0 [Final]",
		"":             "_",
		"nul\x00byte":  "nul_byte",
	} {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArchiveEntry(t *testing.T) {
	for name, want := range map[string]archiveKind{
		"g.zip": kindSevenZip, "g.7z": kindSevenZip, "g.7z.001": kindSevenZip,
		"g.7z.002": notArchive, "g.part1.rar": kindRar, "g.part01.rar": kindRar,
		"g.part2.rar": notArchive, "g.part10.rar": notArchive, "g.rar": kindRar,
		"g.r00": notArchive, "setup.exe": notArchive, "g.apk": notArchive,
	} {
		if got := archiveEntry(name); got != want {
			t.Errorf("archiveEntry(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestExtractedSymlinksAreRemoved(t *testing.T) {
	e := newEnv(t, mkJob("G", "1", mirror(10, 0, "pixeldrain", "https://pixeldrain.com/u/abc", true)))
	outside := t.TempDir()
	script(t, filepath.Dir(mustLook(t, "7zz")), "7zz", `for a; do case "$a" in -o*) d="${a#-o}";; esac; done
mkdir -p "$d/sub" && echo ok > "$d/game.exe"
ln -s "`+outside+`" "$d/escape"
ln -s ../../../etc "$d/sub/rel"`)
	if err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(e.games, "G", "1")
	if _, err := os.Stat(filepath.Join(dest, "game.exe")); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"escape", "sub/rel"} {
		if _, err := os.Lstat(filepath.Join(dest, l)); err == nil {
			t.Fatalf("symlink %s survived", l)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("wrote outside dest: %v", ents)
	}
}

func TestUnsafeURLRejectedForExecHosts(t *testing.T) {
	for _, u := range []string{"https://mega.nz/file/a b", "https://mega.nz/file/a\"b", "https://mega.nz/f\\x", "https://mega.nz/f\nx", "https://mega.nz/f'x"} {
		e := newEnv(t, mkJob("G", "1", mirror(10, 0, "mega", u, true)))
		if err := e.worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		evs := e.tracker.snapshot()
		last := evs[len(evs)-1]
		if last.MirrorState != "failed" || !strings.Contains(last.Error, "url contains") {
			t.Fatalf("%q: last event %+v", u, last)
		}
	}
}
