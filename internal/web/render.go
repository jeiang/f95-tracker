package web

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
)

// render writes page with 200. For an htmx request with a non-empty fragment
// name it writes only that templ.Fragment block of the page component; plain
// requests always get the whole page (forms degrade, R-UI-12).
func (s *Server) render(w http.ResponseWriter, r *http.Request, page templ.Component, fragment string) {
	s.renderStatus(w, r, http.StatusOK, page, fragment)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, page templ.Component, fragment string) {
	var buf bytes.Buffer // buffered so a render failure can still become a 500
	var err error
	if fragment != "" && r.Header.Get("HX-Request") == "true" {
		err = templ.RenderFragments(r.Context(), &buf, page, fragment)
	} else {
		err = page.Render(r.Context(), &buf)
	}
	if err != nil {
		s.log.Error("render", "path", r.URL.Path, "err", err)
		http.Error(w, "Something went wrong. Try again.", http.StatusInternalServerError)
		return
	}
	w.Header().Add("Vary", "HX-Request")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
