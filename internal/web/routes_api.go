package web

import (
	"net/http"

	"github.com/jeiang/f95-tracker/internal/api"
)

// apiRoutes mounts the bearer-token JSON API (internal/api). It is outside the session guard:
// auth.Tokens.Bearer authenticates every /api/ request.
func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.Handle("/api/", api.New(s.downloads, s.tokens, s.log))
}
