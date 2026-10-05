package web

import (
	"net/http"

	"github.com/jeiang/f95-tracker/internal/web/static"
)

func (s *Server) staticRoutes(mux *http.ServeMux) {
	mux.Handle("GET /static/", static.Handler())
}
