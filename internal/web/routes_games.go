package web

import (
	"net/http"

	"github.com/a-h/templ"

	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// gamesRoutes: the list page (owned by the Game list item) and the "/" redirect.
func (s *Server) gamesRoutes(mux *http.ServeMux) {
	mux.Handle("GET /{$}", s.requireSession(http.RedirectHandler("/games", http.StatusFound)))
	// Temporary: renders the empty layout until the Game list lands.
	mux.Handle("GET /games", s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, ui.Layout(ui.PageMeta{Title: "Games", ActiveTab: ui.TabGames}, templ.NopComponent), "")
	})))
}
