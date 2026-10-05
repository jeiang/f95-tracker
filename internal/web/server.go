// Package web wires the HTTP server: router, middleware and the page and partial
// handlers. Each area registers its routes from its own routes_<area>.go.
package web

import (
	"log/slog"
	"net/http"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
)

// Deps are everything handlers may use; later items add fields (services) here.
type Deps struct {
	Store  *db.Store
	Clock  clock.Clock
	Log    *slog.Logger
	Config config.Config
}

type Server struct {
	store *db.Store
	clock clock.Clock
	log   *slog.Logger
	cfg   config.Config
}

func New(d Deps) *Server {
	return &Server{store: d.Store, clock: d.Clock, log: d.Log.With("component", "web"), cfg: d.Config}
}

// Handler returns the full middleware chain around the router.
func (s *Server) Handler() http.Handler {
	cop := http.NewCrossOriginProtection()
	// Bearer-token API routes are exempt from the browser cross-origin check (R-AUTH-6).
	cop.AddInsecureBypassPattern("/api/")
	return s.recoverPanic(s.logRequests(cop.Handler(s.routes())))
}
