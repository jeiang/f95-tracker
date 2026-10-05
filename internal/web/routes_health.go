package web

import (
	"context"
	"net/http"
	"time"
)

// healthRoutes: GET /healthz is open and answers 200 "ok" while the DB answers (R-AUTH-7).
func (s *Server) healthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := s.store.Ping(ctx); err != nil {
			s.log.Error("healthz", "err", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
}
