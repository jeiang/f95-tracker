package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// F95Request is one request the fake received.
type F95Request struct {
	Path      string // URL path as requested (before redirects are followed)
	Query     string
	Cookie    string
	UserAgent string
	At        time.Time
}

// F95Response is a programmed answer; zero Status means 200.
type F95Response struct {
	Status    int
	Body      string
	SetCookie []string
	Location  string // Location header, e.g. for programmed redirects
}

func F95Status(code int) F95Response {
	return F95Response{Status: code, Body: "status " + fmt.Sprint(code)}
}

// F95Challenge is a Cloudflare/DDoS-Guard interstitial.
func F95Challenge() F95Response {
	return F95Response{Status: 503, Body: `<html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt={}</script></body></html>`}
}

// F95Maintenance is a non-forum 200 page announcing maintenance.
func F95Maintenance() F95Response {
	return F95Response{Body: `<html><head><title>Down for maintenance</title></head><body>We are currently undergoing maintenance.</body></html>`}
}

// F95Restricted is XenForo's generic 403 permission page, as served to a logged-in user.
func F95Restricted() F95Response {
	return F95Response{Status: 403, Body: `<!DOCTYPE html><html data-logged-in="true" data-template="error"><head><title>Oops! We ran into some problems.</title></head><body>You do not have permission to view this page or perform this action.</body></html>`}
}

// F95Repeat returns n copies of r, for "429 n times" programs.
func F95Repeat(r F95Response, n int) []F95Response {
	out := make([]F95Response, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// F95Fake serves /sam/checker.php and /threads/<id>/ like F95zone, with
// per-request programmable answers and a request log.
type F95Fake struct {
	Server *httptest.Server

	mu         sync.Mutex
	versions   map[string]string
	threads    map[string]fakeThread
	programs   map[string][]F95Response // key: thread id or "checker"
	validUser  string
	rotate     []string
	log        []F95Request
	cookiePair *regexp.Regexp
}

type fakeThread struct{ loggedIn, guest string }

// NewF95Fake starts the fake; it is closed when the test ends. By default any
// request carrying an xf_user cookie is treated as logged in.
func NewF95Fake(t testing.TB) *F95Fake {
	f := &F95Fake{
		versions: map[string]string{}, threads: map[string]fakeThread{}, programs: map[string][]F95Response{},
		cookiePair: regexp.MustCompile(`(?:^|;\s*)xf_user=([^;]*)`),
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *F95Fake) URL() string { return f.Server.URL }

// SetVersion makes checker.php answer version for id; ids never set are omitted.
func (f *F95Fake) SetVersion(id, version string) {
	f.mu.Lock()
	f.versions[id] = version
	f.mu.Unlock()
}

// SetThread registers the bodies served for a thread to logged-in and guest requests.
func (f *F95Fake) SetThread(id string, loggedIn, guest []byte) {
	f.mu.Lock()
	f.threads[id] = fakeThread{string(loggedIn), string(guest)}
	f.mu.Unlock()
}

// Program queues answers consumed one per request before normal service resumes.
// Use id "checker" for checker.php.
func (f *F95Fake) Program(id string, rs ...F95Response) {
	f.mu.Lock()
	f.programs[id] = append(f.programs[id], rs...)
	f.mu.Unlock()
}

// SetValidUser restricts "logged in" to requests whose xf_user equals value
// (empty = any xf_user); anything else gets the guest body, like an expired cookie.
func (f *F95Fake) SetValidUser(value string) {
	f.mu.Lock()
	f.validUser = value
	f.mu.Unlock()
}

// RotateCookies makes every thread response send these Set-Cookie lines.
func (f *F95Fake) RotateCookies(setCookie ...string) {
	f.mu.Lock()
	f.rotate = setCookie
	f.mu.Unlock()
}

// Requests returns a copy of the request log.
func (f *F95Fake) Requests() []F95Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]F95Request(nil), f.log...)
}

func (f *F95Fake) pop(key string) (F95Response, bool) {
	q := f.programs[key]
	if len(q) == 0 {
		return F95Response{}, false
	}
	f.programs[key] = q[1:]
	return q[0], true
}

func write(w http.ResponseWriter, r F95Response) {
	for _, c := range r.SetCookie {
		w.Header().Add("Set-Cookie", c)
	}
	if r.Location != "" {
		w.Header().Set("Location", r.Location)
	}
	if r.Status == 0 {
		r.Status = 200
	}
	w.WriteHeader(r.Status)
	_, _ = w.Write([]byte(r.Body))
}

var threadPath = regexp.MustCompile(`^/threads/(?:[^/]*\.)?(\d+)/$`)

func (f *F95Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, F95Request{r.URL.Path, r.URL.RawQuery, r.Header.Get("Cookie"), r.UserAgent(), time.Now()})

	switch {
	case r.URL.Path == "/sam/checker.php":
		if p, ok := f.pop("checker"); ok {
			write(w, p)
			return
		}
		ids := strings.Split(r.URL.Query().Get("threads"), ",")
		w.Header().Set("Content-Type", "application/json")
		if len(ids) > 100 {
			_, _ = w.Write([]byte(`{"status":"error","msg":"Invalid threads data or >100"}`))
			return
		}
		msg := map[string]string{}
		for _, id := range ids {
			if v, ok := f.versions[id]; ok {
				msg[id] = v
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "msg": msg})
	case threadPath.MatchString(r.URL.Path):
		id := threadPath.FindStringSubmatch(r.URL.Path)[1]
		if !strings.Contains(strings.TrimSuffix(r.URL.Path, "/"), ".") { // bare /threads/<id>/ redirects to the slug URL
			http.Redirect(w, r, "/threads/some-game-v1."+id+"/", http.StatusMovedPermanently)
			return
		}
		th, ok := f.threads[id]
		if !ok {
			write(w, F95Status(404))
			return
		}
		f.thread(w, r, id, th)
	default:
		http.NotFound(w, r)
	}
}

func (f *F95Fake) thread(w http.ResponseWriter, r *http.Request, key string, th fakeThread) {
	for _, c := range f.rotate {
		w.Header().Add("Set-Cookie", c)
	}
	if p, ok := f.pop(key); ok {
		write(w, p)
		return
	}
	body := th.guest
	if m := f.cookiePair.FindStringSubmatch(r.Header.Get("Cookie")); m != nil && (f.validUser == "" || m[1] == f.validUser) {
		body = th.loggedIn
	}
	write(w, F95Response{Body: body})
}
