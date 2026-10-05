package testutil

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// NtfyRequest is one request the fake ntfy server received.
type NtfyRequest struct {
	Path   string
	Header http.Header
	Body   string
}

// NtfyFake records POSTs and can be told to fail them.
type NtfyFake struct {
	URL string

	mu       sync.Mutex
	requests []NtfyRequest
	failures []int // statuses to answer with, consumed one per request
}

// NewNtfyFake starts a fake ntfy server closed when the test ends.
func NewNtfyFake(t testing.TB) *NtfyFake {
	t.Helper()
	f := &NtfyFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, NtfyRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: string(body)})
		status := http.StatusOK
		if len(f.failures) > 0 {
			status, f.failures = f.failures[0], f.failures[1:]
		}
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// FailNext makes the next n requests answer with status (still recorded).
func (f *NtfyFake) FailNext(n, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for range n {
		f.failures = append(f.failures, status)
	}
}

// Requests returns a copy of everything received so far.
func (f *NtfyFake) Requests() []NtfyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]NtfyRequest(nil), f.requests...)
}
