package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
)

// requireSession guards every route except the open ones (R-AUTH-5): /healthz,
// /auth/* and /static/*. It wraps the whole mux, so routes added later are
// protected without doing anything.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || strings.HasPrefix(p, "/auth/") || strings.HasPrefix(p, "/static/") || s.auth.Authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "HX-Request")
		switch {
		case strings.HasPrefix(p, "/api/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized", "message": "Sign in required."})
		case r.Header.Get("HX-Request") == "true":
			w.Header().Set("HX-Redirect", "/auth/login")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(auth.SafeNext(r.URL.RequestURI())), http.StatusFound)
		default:
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		}
	})
}

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
