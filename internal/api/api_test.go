package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

var ctx = context.Background()

type env struct {
	t       *testing.T
	store   *db.Store
	h       http.Handler
	svc     *downloads.Service
	ntfy    *testutil.NtfyFake
	clk     *clock.Fake
	dl, sub string // bearer tokens by scope
	revoked string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := testutil.NewStore(t)
	clk := clock.NewFake(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	ntfy := testutil.NewNtfyFake(t)
	svc := downloads.New(store, clk, notify.New(store, clk, notify.Options{URL: ntfy.URL, Topic: "t"}))
	tokens := auth.NewTokens(store, clk)
	e := &env{t: t, store: store, svc: svc, ntfy: ntfy, clk: clk}
	e.h = New(svc, tokens, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mk := func(name string, scope auth.Scope) string {
		plain, _, err := tokens.Create(ctx, name, scope)
		if err != nil {
			t.Fatal(err)
		}
		return plain
	}
	e.dl, e.sub = mk("worker", auth.ScopeDownloader), mk("userscript", auth.ScopeSubmitLinks)
	e.revoked = mk("gone", auth.ScopeDownloader)
	toks, _ := tokens.List(ctx)
	for _, tk := range toks {
		if tk.Name == "gone" {
			if err := tokens.Revoke(ctx, tk.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	return e
}

func (e *env) game(name, thread, version string) int64 {
	g, _ := testutil.InsertGame(e.t, e.store, testutil.GameSpec{Name: name, ExternalID: thread, LatestVersion: version, PlayStatus: domain.PlayPlaying})
	return g.ID
}

// call performs a request and decodes the JSON body into a generic map.
func (e *env) call(method, path, token, body string) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		e.t.Fatalf("%s %s: content type %q body %q", method, path, ct, rec.Body.String())
	}
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("%s %s: %v: %q", method, path, err, rec.Body.String())
	}
	return rec.Code, out
}

func (e *env) pushes() int { return len(e.ntfy.Requests()) }

// queuedJob makes a queued job with two supported mirrors via the userscript endpoint.
func (e *env) queuedJob() (jobID int64, thread string) {
	e.t.Helper()
	e.game("Alpha", "1001", "v2.0")
	code, body := e.call("POST", "/api/v1/links", e.sub, `{"thread_id":1001,"links":[
		{"url":"https://mega.nz/file/abc#key","label":"Win/Linux"},
		{"url":"https://pixeldrain.com/u/xyz","platform":"linux"}]}`)
	if code != 200 || body["state"] != "queued" {
		e.t.Fatalf("setup submit: %d %v", code, body)
	}
	return int64(body["job_id"].(float64)), "1001"
}

func errCode(body map[string]any) string { s, _ := body["error"].(string); return s }

func TestAuthAndScopeOnEveryEndpoint(t *testing.T) {
	e := newEnv(t)
	endpoints := []struct{ method, path, body, scope string }{
		{"GET", "/api/v1/downloader/jobs", "", "downloader"},
		{"POST", "/api/v1/downloader/jobs/1/events", `{}`, "downloader"},
		{"GET", "/api/v1/links/pending?thread=1", "", "submit_links"},
		{"POST", "/api/v1/links", `{}`, "submit_links"},
	}
	for _, ep := range endpoints {
		wrong := e.sub
		if ep.scope == "submit_links" {
			wrong = e.dl
		}
		for name, tok := range map[string]string{"missing": "", "garbage": "nope", "revoked": e.revoked} {
			if code, body := e.call(ep.method, ep.path, tok, ep.body); code != 401 || errCode(body) != "unauthorized" {
				t.Errorf("%s %s (%s token): %d %v", ep.method, ep.path, name, code, body)
			}
		}
		if code, body := e.call(ep.method, ep.path, wrong, ep.body); code != 403 || errCode(body) != "forbidden" {
			t.Errorf("%s %s (wrong scope): %d %v", ep.method, ep.path, code, body)
		}
	}
	if code, body := e.call("GET", "/api/v1/nope", e.dl, ""); code != 403 && code != 404 {
		t.Errorf("unknown endpoint: %d %v", code, body)
	}
}

func TestDownloaderJobsList(t *testing.T) {
	e := newEnv(t)
	code, body := e.call("GET", "/api/v1/downloader/jobs", e.dl, "")
	if code != 200 || len(body["jobs"].([]any)) != 0 || len(body["cancelled"].([]any)) != 0 {
		t.Fatalf("empty: %d %v", code, body)
	}
	id, _ := e.queuedJob()
	// A second job waits for links and a third is cancelled: neither shape lists as work.
	g2 := e.game("Beta", "1002", "v1")
	_, _, _ = e.svc.Create(ctx, g2)
	g3 := e.game("Gamma", "1003", "v1")
	j3, _, _ := e.svc.Create(ctx, g3)
	if _, err := e.svc.Cancel(ctx, j3.ID); err != nil {
		t.Fatal(err)
	}

	code, body = e.call("GET", "/api/v1/downloader/jobs", e.dl, "")
	jobs := body["jobs"].([]any)
	if code != 200 || len(jobs) != 1 {
		t.Fatalf("%d %v", code, body)
	}
	j := jobs[0].(map[string]any)
	game := j["game"].(map[string]any)
	if int64(j["id"].(float64)) != id || game["name"] != "Alpha" || j["target_version"] != "v2.0" || j["state"] != "queued" || j["effective_platform_pref"] != "linux" {
		t.Fatalf("job: %v", j)
	}
	ms := j["mirrors"].([]any)
	if len(ms) != 2 {
		t.Fatalf("mirrors: %v", ms)
	}
	m0, m1 := ms[0].(map[string]any), ms[1].(map[string]any)
	// Linux preferred: the pixeldrain Linux mirror precedes the mega Win/Linux one.
	if m0["host"] != "pixeldrain" || m0["platform"] != "linux" || m0["position"].(float64) != 0 || m0["supported"] != true || m0["state"] != "pending" ||
		m0["url"] != "https://pixeldrain.com/u/xyz" || m0["id"] == nil {
		t.Fatalf("mirror 0: %v", m0)
	}
	if m1["host"] != "mega" || m1["platform"] != "win_linux" || m1["position"].(float64) != 1 {
		t.Fatalf("mirror 1: %v", m1)
	}
	if c := body["cancelled"].([]any); len(c) != 1 || int64(c[0].(float64)) != j3.ID {
		t.Fatalf("cancelled: %v", c)
	}
}

func TestJobEventsHappyPathAndShape(t *testing.T) {
	e := newEnv(t)
	id, _ := e.queuedJob()
	path := "/api/v1/downloader/jobs/" + itoa(id) + "/events"
	_, list := e.call("GET", "/api/v1/downloader/jobs", e.dl, "")
	m0 := int64(list["jobs"].([]any)[0].(map[string]any)["mirrors"].([]any)[0].(map[string]any)["id"].(float64))

	code, body := e.call("POST", path, e.dl, `{"state":"downloading","mirror_id":`+itoa(m0)+`,"mirror_state":"active","bytes_done":5,"bytes_total":50}`)
	job, _ := body["job"].(map[string]any)
	if code != 200 || job["state"] != "downloading" || job["bytes_done"].(float64) != 5 || job["bytes_total"].(float64) != 50 || job["active_mirror_id"].(float64) != float64(m0) {
		t.Fatalf("downloading: %d %v", code, body)
	}
	if code, body = e.call("POST", path, e.dl, `{"state":"extracting"}`); code != 200 {
		t.Fatalf("extracting: %d %v", code, body)
	}
	code, body = e.call("POST", path, e.dl, `{"state":"done","result_path":"/games/Alpha/v2.0"}`)
	if code != 200 || body["job"].(map[string]any)["state"] != "done" {
		t.Fatalf("done: %d %v", code, body)
	}
	if e.pushes() != 0 {
		t.Fatalf("pushes: %d", e.pushes())
	}
}

func TestJobEventErrors(t *testing.T) {
	e := newEnv(t)
	id, _ := e.queuedJob()
	path := "/api/v1/downloader/jobs/" + itoa(id) + "/events"
	cases := []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"illegal transition", path, `{"state":"done"}`, 409, "illegal_transition"},
		{"worker cancels", path, `{"state":"cancelled"}`, 409, "illegal_transition"},
		{"unknown job", "/api/v1/downloader/jobs/9999/events", `{"state":"downloading"}`, 404, "not_found"},
		{"non numeric job", "/api/v1/downloader/jobs/abc/events", `{}`, 404, "not_found"},
		{"unknown mirror", path, `{"mirror_id":9999,"mirror_state":"active"}`, 404, "not_found"},
		{"bad state", path, `{"state":"exploded"}`, 422, "invalid"},
		{"needs_human without reason", path, `{"state":"needs_human"}`, 422, "invalid"},
		{"not json", path, `{`, 422, "invalid"},
	}
	for _, c := range cases {
		code, body := e.call("POST", c.path, e.dl, c.body)
		if code != c.status || errCode(body) != c.code || body["message"] == nil {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, body, c.status, c.code)
		}
	}
	if j, _ := e.svc.Get(ctx, id); j.State != downloads.Queued {
		t.Fatalf("failed events moved the job to %s", j.State)
	}
}

func TestLastMirrorFailedMovesToNeedsHumanOnce(t *testing.T) {
	e := newEnv(t)
	id, _ := e.queuedJob()
	path := "/api/v1/downloader/jobs/" + itoa(id) + "/events"
	j, _ := e.svc.Get(ctx, id)
	m0, m1 := j.Mirrors[0].ID, j.Mirrors[1].ID

	code, body := e.call("POST", path, e.dl, `{"state":"queued","mirror_id":`+itoa(m0)+`,"mirror_state":"failed","error":"404"}`)
	if code != 200 || body["job"].(map[string]any)["state"] != "queued" {
		t.Fatalf("first failure: %d %v", code, body)
	}
	code, body = e.call("POST", path, e.dl, `{"state":"queued","mirror_id":`+itoa(m1)+`,"mirror_state":"failed","error":"quota"}`)
	job := body["job"].(map[string]any)
	if code != 200 || job["state"] != "needs_human" || job["needs_human_reason"] != "all mirrors failed" {
		t.Fatalf("last failure: %d %v", code, body)
	}
	if e.pushes() != 1 {
		t.Fatalf("pushes = %d", e.pushes())
	}
	// needs_human jobs are not work for the worker.
	_, list := e.call("GET", "/api/v1/downloader/jobs", e.dl, "")
	if len(list["jobs"].([]any)) != 0 {
		t.Fatalf("needs_human listed: %v", list)
	}
}

func TestLinksPending(t *testing.T) {
	e := newEnv(t)
	g := e.game("Waiting", "2001", "v7")
	e.game("Idle", "2002", "v1")
	if code, body := e.call("GET", "/api/v1/links/pending?thread=2001", e.sub, ""); code != 404 || errCode(body) != "not_found" {
		t.Fatalf("no job yet: %d %v", code, body)
	}
	j, _, err := e.svc.Create(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	code, body := e.call("GET", "/api/v1/links/pending?thread=2001", e.sub, "")
	if code != 200 || int64(body["job_id"].(float64)) != j.ID || body["game_name"] != "Waiting" || body["target_version"] != "v7" {
		t.Fatalf("pending: %d %v", code, body)
	}
	for name, q := range map[string]string{"unknown thread": "?thread=9", "idle game": "?thread=2002"} {
		if code, _ := e.call("GET", "/api/v1/links/pending"+q, e.sub, ""); code != 404 {
			t.Errorf("%s: %d", name, code)
		}
	}
	for name, q := range map[string]string{"missing": "", "not a number": "?thread=abc"} {
		if code, body := e.call("GET", "/api/v1/links/pending"+q, e.sub, ""); code != 422 {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
}

func TestSubmitLinks(t *testing.T) {
	e := newEnv(t)
	e.game("Alpha", "3001", "v1")
	body := `{"thread_id":3001,"links":[
		{"url":"https://pixeldrain.com/u/a","label":"Linux"},
		{"url":"https://mega.io/file/b#k","platform":"win"},
		{"url":"https://workupload.com/f/c"}]}`
	code, res := e.call("POST", "/api/v1/links", e.sub, body)
	if code != 200 || res["state"] != "queued" || res["accepted"].(float64) != 3 || res["manual"].(float64) != 1 || res["job_id"] == nil {
		t.Fatalf("submit: %d %v", code, res)
	}
	j, _ := e.svc.Get(ctx, int64(res["job_id"].(float64)))
	if j.TargetVersion != "v1" || len(j.Mirrors) != 3 || j.Mirrors[2].Host != "workupload.com" || j.Mirrors[2].State != "manual" {
		t.Fatalf("stored job: %+v", j)
	}

	// 409: the Game's job is already past link submission.
	if code, res := e.call("POST", "/api/v1/links", e.sub, body); code != 409 || errCode(res) != "conflict" {
		t.Errorf("resubmit: %d %v", code, res)
	}
}

func TestSubmitLinksRejections(t *testing.T) {
	e := newEnv(t)
	e.game("Alpha", "4001", "v1")
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"masked", `{"thread_id":4001,"links":[{"url":"https://pixeldrain.com/u/a"},{"url":"https://f95zone.to/masked/abc/"}]}`, 422, "masked_url"},
		{"unknown thread", `{"thread_id":99,"links":[{"url":"https://pixeldrain.com/u/a"}]}`, 404, "not_found"},
		{"no links", `{"thread_id":4001,"links":[]}`, 422, "invalid"},
		{"bad url", `{"thread_id":4001,"links":[{"url":"nonsense"}]}`, 422, "invalid"},
		{"bad platform", `{"thread_id":4001,"links":[{"url":"https://mega.nz/file/a","platform":"beos"}]}`, 422, "invalid"},
		{"no thread id", `{"links":[{"url":"https://mega.nz/file/a"}]}`, 422, "invalid"},
		{"not json", `nope`, 422, "invalid"},
	}
	for _, c := range cases {
		if code, res := e.call("POST", "/api/v1/links", e.sub, c.body); code != c.status || errCode(res) != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, res, c.status, c.code)
		}
	}
	// None of the rejected submissions stored anything or created a job.
	jobs, _ := e.svc.List(ctx)
	if len(jobs) != 0 {
		t.Fatalf("rejected submissions left jobs: %+v", jobs)
	}
}

func TestSubmitOnlyUnsupportedHostsNeedsHuman(t *testing.T) {
	e := newEnv(t)
	e.game("Thread 12760", "12760", "v1")
	code, res := e.call("POST", "/api/v1/links", e.sub, `{"thread_id":12760,"links":[{"url":"https://workupload.com/f"}]}`)
	if code != 200 || res["state"] != "needs_human" || res["accepted"].(float64) != 1 || res["manual"].(float64) != 1 {
		t.Fatalf("%d %v", code, res)
	}
	if e.pushes() != 1 {
		t.Fatalf("pushes: %d", e.pushes())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
