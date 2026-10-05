package web

import (
	"log/slog"
	"net/http"
	"time"
)

// requireSession is the one intentional placeholder: it lets every request
// through until the auth item (C5) replaces it with the real session check.
func (s *Server) requireSession(next http.Handler) http.Handler { return next }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and friends.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests logs method, path (never the query string), status and duration.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clock.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", s.clock.Now().Sub(start).Round(time.Microsecond).String())
	})
}

// recoverPanic turns a handler panic into a 500 without killing the process.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", p)
				s.respondError(w, r, errInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
