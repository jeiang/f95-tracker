// Package web wires the HTTP server: router, middleware and the page and partial
// handlers. Each area registers its routes from its own routes_<area>.go.
package web

import (
	"log/slog"
	"net/http"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/tags"
)

// Deps are everything handlers may use; later items add fields (services) here.
type Deps struct {
	Store     *db.Store
	Clock     clock.Clock
	Log       *slog.Logger
	Config    config.Config
	Auth      *auth.Auth
	Games     *games.Service
	Tags      *tags.Service
	Notify    *notify.Notifier
	Itch      *itch.Client
	F95       *f95.Client
	Refresher *check.Refresher
}

type Server struct {
	store     *db.Store
	clock     clock.Clock
	log       *slog.Logger
	cfg       config.Config
	auth      *auth.Auth
	games     *games.Service
	tags      *tags.Service
	notify    *notify.Notifier
	itch      *itch.Client
	f95       *f95.Client
	refresher *check.Refresher
}

func New(d Deps) *Server {
	return &Server{
		store: d.Store, clock: d.Clock, log: d.Log.With("component", "web"), cfg: d.Config,
		auth: d.Auth, games: d.Games, tags: d.Tags, notify: d.Notify, itch: d.Itch, f95: d.F95,
		refresher: d.Refresher,
	}
}

// Handler returns the full middleware chain around the router.
func (s *Server) Handler() http.Handler {
	cop := http.NewCrossOriginProtection()
	// Bearer-token API routes are exempt from the browser cross-origin check (R-AUTH-6).
	cop.AddInsecureBypassPattern("/api/")
	return s.recoverPanic(s.logRequests(cop.Handler(s.auth.Middleware(s.requireSession(s.routes())))))
}
