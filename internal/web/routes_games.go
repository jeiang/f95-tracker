package web

import (
	"net/http"

	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// gamesRoutes: the list page and the "/" redirect.
func (s *Server) gamesRoutes(mux *http.ServeMux) {
	mux.Handle("GET /{$}", s.requireSession(http.RedirectHandler("/games", http.StatusFound)))
	mux.Handle("GET /games", s.requireSession(http.HandlerFunc(s.gameList)))
}

func (s *Server) gameList(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildGameList(r.Context(), r.URL.Query())
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabGames, "Games"), ui.GamesListPage(v)), "game-table")
}
