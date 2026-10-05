package f95

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

const browserUA = "Mozilla/5.0 (X11; Linux x86_64) TestBrowser/1.0"

type env struct {
	fake  *testutil.F95Fake
	creds *CredStore
	clk   *clock.Fake
	slept []time.Duration
	mu    sync.Mutex
}

func (e *env) sleep(_ context.Context, d time.Duration) error {
	e.mu.Lock()
	e.slept = append(e.slept, d)
	e.mu.Unlock()
	e.clk.Advance(d)
	return nil
}

// newEnv wires a fake F95, a migrated store and a stored cookie (xf_user=user1,
// xf_tfa_trust=trust1) with the test browser UA.
func newEnv(t *testing.T, withCookie bool) *env {
	t.Helper()
	e := &env{
		fake: testutil.NewF95Fake(t),
		clk:  clock.NewFake(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)),
	}
	e.creds = NewCredStore(testutil.NewStore(t), e.clk)
	if withCookie {
		if err := e.creds.Replace(context.Background(), Jar{"xf_user": "user1", "xf_tfa_trust": "trust1"}, browserUA, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *env) client(t *testing.T, mod func(*Options)) *Client {
	t.Helper()
	o := Options{BaseURL: e.fake.URL(), Creds: e.creds, Sleep: e.sleep, Version: "test"}
	if mod != nil {
		mod(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func (e *env) loadThread(t *testing.T, id, loggedInFile, guestFile string) {
	t.Helper()
	e.fake.SetThread(id, readFixture(t, loggedInFile), readFixture(t, guestFile))
}

func TestCheckVersionsNeverSendsCookie(t *testing.T) {
	e := newEnv(t, true)
	e.fake.SetVersion("67494", "v4.34.1 Public")
	e.fake.SetVersion("59416", "v0.13.0")
	got, err := e.client(t, nil).CheckVersions(context.Background(), []string{"67494", "59416", "94891"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["67494"] != "v4.34.1 Public" || got["59416"] != "v0.13.0" {
		t.Fatalf("versions = %v (restricted id must be absent)", got)
	}
	for _, r := range e.fake.Requests() {
		if r.Cookie != "" {
			t.Errorf("cookie sent to %s", r.Path)
		}
		if r.UserAgent != browserUA {
			t.Errorf("UA = %q, want stored browser UA", r.UserAgent)
		}
	}
}

func TestUserAgentFallbackWithoutCredential(t *testing.T) {
	e := newEnv(t, false)
	e.fake.SetVersion("1", "v1")
	if _, err := e.client(t, nil).CheckVersions(context.Background(), []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if ua := e.fake.Requests()[0].UserAgent; ua != "f95-tracker/test (+https://github.com/jeiang/f95-tracker)" {
		t.Errorf("UA = %q", ua)
	}
}

func TestCheckVersionsBatches(t *testing.T) {
	e := newEnv(t, false)
	var ids []string
	for i := 1; i <= 250; i++ {
		id := fmt.Sprint(i)
		ids = append(ids, id)
		e.fake.SetVersion(id, "v"+id)
	}
	got, err := e.client(t, nil).CheckVersions(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	reqs := e.fake.Requests()
	if len(got) != 250 || len(reqs) != 3 {
		t.Fatalf("got %d versions in %d calls, want 250 in 3", len(got), len(reqs))
	}
	for _, r := range reqs {
		if n := strings.Count(r.Query, "%2C") + 1; n > 100 {
			t.Errorf("batch of %d ids", n)
		}
	}
}

func TestCheckVersionsParseSanity(t *testing.T) {
	for name, tc := range map[string]struct {
		resp testutil.F95Response
		want error
	}{
		"status error":        {testutil.F95Response{Body: `{"status":"error","msg":"Invalid threads data or >100"}`}, ErrParse},
		"empty answer":        {testutil.F95Response{Body: `{"status":"ok","msg":[]}`}, ErrParse},
		"not json":            {testutil.F95Response{Body: `<html>oops</html>`}, ErrParse},
		"temporarily blocked": {testutil.F95Response{Body: `{"status":"error","msg":"You have been temporarily blocked because of a large amount of requests, please try again later"}`}, ErrBlocked},
		"429":                 {testutil.F95Status(429), ErrBlocked},
		"challenge":           {testutil.F95Challenge(), ErrBlocked},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, false)
			e.fake.Program("checker", tc.resp)
			_, err := e.client(t, nil).CheckVersions(context.Background(), []string{"1"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRetryLadder(t *testing.T) {
	ladder := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
	t.Run("recovers", func(t *testing.T) {
		e := newEnv(t, false)
		e.fake.SetVersion("1", "v1")
		e.fake.Program("checker", testutil.F95Repeat(testutil.F95Status(429), 2)...)
		start := e.clk.Now()
		got, err := e.client(t, func(o *Options) { o.Retry = ladder }).CheckVersions(context.Background(), []string{"1"})
		if err != nil || got["1"] != "v1" {
			t.Fatalf("got %v, %v", got, err)
		}
		if fmt.Sprint(e.slept) != fmt.Sprint(ladder[:2]) || e.clk.Now().Sub(start) != 6*time.Minute {
			t.Errorf("slept %v", e.slept)
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		e := newEnv(t, true)
		e.loadThread(t, "67494", "67494.html", "59416.guest.html")
		e.fake.Program("67494", testutil.F95Repeat(testutil.F95Status(503), 10)...)
		_, err := e.client(t, func(o *Options) { o.Retry = ladder }).FetchThread(context.Background(), "67494")
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v", err)
		}
		attempts := 0
		for _, r := range e.fake.Requests() {
			if strings.Contains(r.Path, ".") { // the slug URL; the bare id URL is the redirect hop
				attempts++
			}
		}
		if fmt.Sprint(e.slept) != fmt.Sprint(ladder) || attempts != 4 {
			t.Errorf("slept %v over %d attempts", e.slept, attempts)
		}
	})
	t.Run("interactive fails once", func(t *testing.T) {
		e := newEnv(t, false)
		e.fake.Program("checker", testutil.F95Repeat(testutil.F95Status(429), 3)...)
		_, err := e.client(t, nil).CheckVersions(context.Background(), []string{"1"})
		if !errors.Is(err, ErrBlocked) || len(e.slept) != 0 || len(e.fake.Requests()) != 1 {
			t.Fatalf("err %v slept %v requests %d", err, e.slept, len(e.fake.Requests()))
		}
	})
	t.Run("maintenance page", func(t *testing.T) {
		e := newEnv(t, true)
		e.loadThread(t, "67494", "67494.html", "59416.guest.html")
		e.fake.Program("67494", testutil.F95Maintenance())
		if _, err := e.client(t, nil).FetchThread(context.Background(), "67494"); !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestFetchThreadSendsCookieAndPersistsRotation(t *testing.T) {
	e := newEnv(t, true)
	e.loadThread(t, "67494", "67494.html", "59416.guest.html")
	e.fake.RotateCookies(
		"xf_session=newsession; Path=/; Secure; HttpOnly",
		"xf_csrf=newcsrf; Path=/",
		"xf__sam_ad_views=5; Path=/",
		"cf_clearance=nope; Path=/",
	)
	th, err := e.client(t, nil).FetchThread(context.Background(), "67494")
	if err != nil {
		t.Fatal(err)
	}
	if !th.LoggedIn || !th.HasGenre || th.Name != "Out of Touch!" {
		t.Fatalf("thread = %+v", th)
	}
	if !strings.Contains(th.URL, "/threads/some-game-v1.67494/") {
		t.Errorf("redirect not followed: %s", th.URL)
	}
	reqs := e.fake.Requests()
	if len(reqs) != 2 || reqs[0].Cookie != "xf_user=user1; xf_tfa_trust=trust1" {
		t.Fatalf("requests = %+v", reqs)
	}
	cred, _, _ := e.creds.Load(context.Background())
	want := Jar{"xf_user": "user1", "xf_tfa_trust": "trust1", "xf_session": "newsession", "xf_csrf": "newcsrf"}
	if fmt.Sprint(cred.Jar) != fmt.Sprint(want) {
		t.Errorf("jar = %v, want %v", cred.Jar, want)
	}
	row, _ := e.creds.Get(context.Background())
	if row.Validity != string(domain.CookieValid) || !row.ValidatedAt.Valid {
		t.Errorf("validity = %q", row.Validity)
	}
	// the rotated jar is what the next request sends
	if _, err := e.client(t, nil).FetchThread(context.Background(), "67494"); err != nil {
		t.Fatal(err)
	}
	if c := e.fake.Requests()[2].Cookie; !strings.Contains(c, "xf_session=newsession") {
		t.Errorf("second request cookie = %q", c)
	}
}

func TestFetchThreadGuest(t *testing.T) {
	e := newEnv(t, false)
	e.loadThread(t, "59416", "67494.html", "59416.guest.html")
	th, err := e.client(t, nil).FetchThread(context.Background(), "59416")
	if err != nil {
		t.Fatal(err)
	}
	if th.LoggedIn || th.HasGenre || th.Version != "v0.13.0" {
		t.Fatalf("thread = %+v", th)
	}
	for _, r := range e.fake.Requests() {
		if r.Cookie != "" {
			t.Error("guest request carried a cookie")
		}
	}
}

func TestInvalidCookieFlipsValidityOnce(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	e.loadThread(t, "67494", "67494.html", "59416.guest.html")
	c := e.client(t, nil)
	if _, err := c.FetchThread(ctx, "67494"); err != nil {
		t.Fatal(err)
	}
	e.fake.SetValidUser("someone-else") // the stored cookie no longer logs in
	if _, err := c.FetchThread(ctx, "67494"); !errors.Is(err, ErrCookieInvalid) {
		t.Fatalf("err = %v, want ErrCookieInvalid", err)
	}
	row, _ := e.creds.Get(ctx)
	if row.Validity != string(domain.CookieInvalid) {
		t.Fatalf("validity = %q", row.Validity)
	}
	// a second failure is not a new transition
	if _, err := c.FetchThread(ctx, "67494"); !errors.Is(err, ErrCookieInvalid) {
		t.Fatal(err)
	}
	if flipped, err := e.creds.MarkInvalid(ctx); err != nil || flipped {
		t.Errorf("already invalid: flipped=%v err=%v", flipped, err)
	}
	// alert bookkeeping is re-armed by the next valid load
	if err := e.creds.MarkAlerted(ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.SetValidUser("")
	if _, err := c.FetchThread(ctx, "67494"); err != nil {
		t.Fatal(err)
	}
	row, _ = e.creds.Get(ctx)
	if row.Validity != "valid" || row.InvalidAlertedAt.Valid {
		t.Errorf("after recovery: %+v", row)
	}
}

func TestRestrictedThread(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	e.loadThread(t, "67494", "67494.html", "59416.guest.html")
	e.fake.SetThread("94891", readFixture(t, "94891.html"), readFixture(t, "94891.html"))
	e.fake.Program("94891", testutil.F95Response{Status: 403, Body: string(readFixture(t, "94891.html"))})
	c := e.client(t, nil)
	if _, err := c.FetchThread(ctx, "67494"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchThread(ctx, "94891"); !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want ErrRestricted", err)
	}
	if row, _ := e.creds.Get(ctx); row.Validity != "valid" {
		t.Errorf("a restricted thread must not invalidate the cookie, validity = %q", row.Validity)
	}
}

func TestCookieNeverLeavesBaseHost(t *testing.T) {
	e := newEnv(t, true)
	var hit bool
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer foreign.Close()
	e.fake.SetThread("1", []byte("x"), []byte("x"))
	e.fake.Program("1", testutil.F95Response{Status: 302, Location: foreign.URL + "/steal"})
	if _, err := e.client(t, nil).FetchThread(context.Background(), "1"); err == nil {
		t.Fatal("redirect to a foreign host was followed")
	}
	if hit {
		t.Error("foreign host received a request")
	}
}

func TestResponseCap(t *testing.T) {
	e := newEnv(t, false)
	e.fake.SetThread("1", nil, nil)
	e.fake.Program("1", testutil.F95Response{Body: strings.Repeat("a", maxBody+1)})
	if _, err := e.client(t, nil).FetchThread(context.Background(), "1"); !errors.Is(err, ErrParse) {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeAndSaveAndValidate(t *testing.T) {
	ctx := context.Background()
	t.Run("no credential makes no request", func(t *testing.T) {
		e := newEnv(t, false)
		ok, err := e.client(t, nil).Probe(ctx, "67494")
		if ok || err != nil || len(e.fake.Requests()) != 0 {
			t.Fatalf("ok=%v err=%v requests=%d", ok, err, len(e.fake.Requests()))
		}
	})
	t.Run("save and validate", func(t *testing.T) {
		e := newEnv(t, false)
		e.fake.SetValidUser("abc")
		e.loadThread(t, "67494", "67494.html", "59416.guest.html")
		c := e.client(t, nil)
		raw := "Cookie: xf_user=abc; cf_clearance=zzz; xf_session=s1; other=1"
		ok, err := c.SaveAndValidate(ctx, raw, browserUA, time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), "67494")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		row, _ := e.creds.Get(ctx)
		if row.Validity != "valid" || row.TfaTrustExpiresAt.String != "2026-10-26" || row.UserAgent != browserUA {
			t.Errorf("row = %+v", row)
		}
		if r := e.fake.Requests()[0]; r.Path != "/threads/67494/" {
			t.Errorf("probe hit %s, want the thread page", r.Path)
		}
		if c := e.fake.Requests()[0].Cookie; c != "xf_user=abc; xf_session=s1" {
			t.Errorf("cookie sent = %q (only kept names)", c)
		}
		// pasting a stale cookie reports invalid without an error
		ok, err = c.SaveAndValidate(ctx, "xf_user=stale; xf_tfa_trust=t", browserUA, time.Time{}, "67494")
		if ok || err != nil {
			t.Fatalf("stale: ok=%v err=%v", ok, err)
		}
		if row, _ := e.creds.Get(ctx); row.Validity != "invalid" {
			t.Errorf("validity = %q", row.Validity)
		}
	})
	t.Run("unusable paste is rejected before saving", func(t *testing.T) {
		e := newEnv(t, false)
		_, err := e.client(t, nil).SaveAndValidate(ctx, "xf_user=only", browserUA, time.Time{}, "67494")
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("err = %v", err)
		}
		if _, ok, _ := e.creds.Load(ctx); ok {
			t.Error("rejected paste was stored")
		}
	})
}

func TestParseCookieHeader(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want Jar
		bad  bool
	}{
		"full with prefix":  {"Cookie: xf_user=u; xf_tfa_trust=t; xf_session=s; xf_csrf=c; cf_clearance=z", Jar{"xf_user": "u", "xf_tfa_trust": "t", "xf_session": "s", "xf_csrf": "c"}, false},
		"user plus session": {"xf_user=u; xf_session=s", Jar{"xf_user": "u", "xf_session": "s"}, false},
		"value with equals": {"xf_user=a=b==; xf_tfa_trust=t", Jar{"xf_user": "a=b==", "xf_tfa_trust": "t"}, false},
		"lowercase prefix":  {"cookie:  xf_user=u ;xf_tfa_trust=t ", Jar{"xf_user": "u", "xf_tfa_trust": "t"}, false},
		"user alone (414)":  {"xf_user=u", nil, true},
		"no user":           {"xf_tfa_trust=t; xf_session=s", nil, true},
		"empty user value":  {"xf_user=; xf_tfa_trust=t", nil, true},
		"garbage":           {"hello", nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCookieHeader(tc.raw)
			if tc.bad {
				if !errors.Is(err, domain.ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestPacerSerializesAcrossClients(t *testing.T) {
	e := newEnv(t, false)
	e.loadThread(t, "67494", "67494.html", "59416.guest.html")
	lock := t.TempDir() + "/f95.lock"
	const gap = 200 * time.Millisecond
	mk := func() *Client {
		return e.client(t, func(o *Options) { o.Pacer = &Pacer{LockPath: lock, Spacing: gap} }) // separate Pacer = separate open file
	}
	a, b := mk(), mk()
	var wg sync.WaitGroup
	for _, c := range []*Client{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.FetchThread(context.Background(), "67494"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var starts []time.Time
	for _, r := range e.fake.Requests() {
		if strings.HasPrefix(r.Path, "/threads/") && !strings.Contains(r.Path, ".") { // first hop of each fetch
			starts = append(starts, r.At)
		}
	}
	if len(starts) != 4 {
		t.Fatalf("%d fetches", len(starts))
	}
	sortTimes(starts)
	for i := 1; i < len(starts); i++ {
		if d := starts[i].Sub(starts[i-1]); d < gap-20*time.Millisecond {
			t.Errorf("requests %d and %d only %v apart, want >= %v", i-1, i, d, gap)
		}
	}
}

func TestPacerBusyForInteractiveCaller(t *testing.T) {
	e := newEnv(t, false)
	e.fake.SetVersion("1", "v1")
	lock := t.TempDir() + "/f95.lock"
	holder := e.client(t, func(o *Options) { o.Pacer = &Pacer{LockPath: lock, Spacing: 600 * time.Millisecond} })
	if _, err := holder.CheckVersions(context.Background(), []string{"1"}); err != nil {
		t.Fatal(err)
	}
	waiter := e.client(t, func(o *Options) {
		o.Interactive = true
		o.Pacer = &Pacer{LockPath: lock, Spacing: 0, MaxWait: 80 * time.Millisecond}
	})
	if _, err := waiter.CheckVersions(context.Background(), []string{"1"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	// a non-interactive caller waits the hold out instead
	patient := e.client(t, func(o *Options) { o.Pacer = &Pacer{LockPath: lock, Spacing: 0} })
	if _, err := patient.CheckVersions(context.Background(), []string{"1"}); err != nil {
		t.Fatal(err)
	}
}

func sortTimes(ts []time.Time) {
	for i := range ts {
		for j := i + 1; j < len(ts); j++ {
			if ts[j].Before(ts[i]) {
				ts[i], ts[j] = ts[j], ts[i]
			}
		}
	}
}

func TestProbeThreadChoice(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	if id, err := e.creds.ProbeThread(ctx); err != nil || id != fallbackProbeThread {
		t.Fatalf("empty db: %q, %v", id, err)
	}
	store := testutil.NewStore(t)
	cs := NewCredStore(store, e.clk)
	testutil.InsertGame(t, store, testutil.GameSpec{Name: "A", ExternalID: "111"})
	testutil.InsertGame(t, store, testutil.GameSpec{Name: "B", ExternalID: "222"})
	if id, err := cs.ProbeThread(ctx); err != nil || id != "111" {
		t.Fatalf("oldest source: %q, %v", id, err)
	}
}
