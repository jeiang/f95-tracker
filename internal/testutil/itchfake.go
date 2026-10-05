package testutil

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ItchRequest is one request the fake received.
type ItchRequest struct {
	Path      string
	UserAgent string
	Cookie    string
}

// ItchFake is an httptest itch.io: pages and devlog feeds by URL path, with
// programmable one-shot status responses and request recording.
type ItchFake struct {
	URL string

	mu       sync.Mutex
	bodies   map[string][]byte
	scripted map[string][]int
	reqs     []ItchRequest
}

func NewItchFake(t testing.TB) *ItchFake {
	f := &ItchFake{bodies: map[string][]byte{}, scripted: map[string][]int{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Set serves body (200) at path, e.g. "/lycoris-radiata" or "/lycoris-radiata/devlog.rss".
// Unset paths answer 404.
func (f *ItchFake) Set(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies[path] = body
}

// Respond queues status codes answered (empty body) before normal serving resumes.
func (f *ItchFake) Respond(path string, statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripted[path] = append(f.scripted[path], statuses...)
}

// Requests returns a copy of the recorded requests in arrival order.
func (f *ItchFake) Requests() []ItchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ItchRequest(nil), f.reqs...)
}

func (f *ItchFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, ItchRequest{r.URL.Path, r.UserAgent(), r.Header.Get("Cookie")})
	if q := f.scripted[r.URL.Path]; len(q) > 0 {
		f.scripted[r.URL.Path] = q[1:]
		f.mu.Unlock()
		w.WriteHeader(q[0])
		return
	}
	body, ok := f.bodies[r.URL.Path]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(body)
}
