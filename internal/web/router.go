package web

import "net/http"

// routes builds the mux. Each area registers its patterns in its own file; routes
// that need a session wrap their handler in s.requireSession.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	s.healthRoutes(mux)
	s.staticRoutes(mux)
	s.authRoutes(mux)
	s.gamesRoutes(mux)
	s.gameRoutes(mux)
	s.addRoutes(mux)
	s.reviewRoutes(mux)
	s.importRoutes(mux)
	s.settingsRoutes(mux)
	return mux
}
