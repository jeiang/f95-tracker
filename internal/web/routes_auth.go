package web

import (
	"errors"
	"net/http"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// authRoutes registers the open /auth/* routes (R-AUTH-1..6).
func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		dest, err := s.auth.BeginLogin(r.Context(), w, r.URL.Query().Get("next"))
		if err != nil {
			s.authFailure(w, r, err)
			return
		}
		http.Redirect(w, r, dest, http.StatusFound)
	})
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next, err := s.auth.CompleteLogin(w, r)
		if err != nil {
			s.authFailure(w, r, err)
			return
		}
		http.Redirect(w, r, next, http.StatusFound)
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if err := s.auth.Logout(r); err != nil {
			s.respondError(w, r, err)
			return
		}
		s.authPage(w, r, http.StatusOK, "Signed out", "You are signed out. Open the tracker again to sign in.")
	})
}

func (s *Server) authFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		s.authPage(w, r, http.StatusForbidden, "Forbidden", "This account is not allowed to use this tracker.")
	case errors.Is(err, auth.ErrInvalid):
		s.authPage(w, r, http.StatusBadRequest, "Sign-in failed", "The sign-in response was not valid. Start again from the tracker.")
	case errors.Is(err, auth.ErrProvider):
		s.log.Error("sign-in", "err", err)
		s.authPage(w, r, http.StatusBadGateway, "Sign-in unavailable", "The identity provider could not be reached. Try again later.")
	default:
		s.respondError(w, r, err)
	}
}

func (s *Server) authPage(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	s.renderStatus(w, r, status, ui.Layout(ui.PageMeta{Title: title}, ui.ErrorPage(title, msg)), "")
}
