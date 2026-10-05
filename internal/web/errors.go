package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

var errInternal = errors.New("internal error")

// errorInfo maps an error to its HTTP status and API error code (spec §5.4).
// Items that add error sentinels (internal/f95: 502 for F95 failures) extend the
// switch. Anything unknown is a 500.
func errorInfo(err error) (status int, code string) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrIllegalTransition):
		return http.StatusConflict, "illegal_transition"
	case errors.Is(err, domain.ErrValidation):
		return http.StatusUnprocessableEntity, "validation"
	}
	return http.StatusInternalServerError, "internal"
}

// respondError answers err for the kind of client that asked: JSON for /api/,
// an #error-slot fragment for htmx (R-UI-12), a plain error page otherwise.
// 5xx messages are generic; 4xx messages are the error text (domain errors are
// written for users). Stack traces and causes are only logged.
func (s *Server) respondError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := errorInfo(err)
	msg := err.Error()
	if status >= 500 {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		msg = "Something went wrong. Try again."
	} else if status == http.StatusNotFound {
		msg = "That page or Game does not exist."
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Retarget", "#error-slot")
		w.Header().Set("HX-Reswap", "innerHTML")
		s.renderStatus(w, r, status, ui.FormError(msg), "")
	default:
		meta := s.pageMeta(r, ui.TabNone, http.StatusText(status))
		s.renderStatus(w, r, status, ui.Layout(meta, ui.ErrorPage(http.StatusText(status), msg)), "")
	}
}
