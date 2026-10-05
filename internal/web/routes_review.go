package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// reviewRoutes: the Tag review page (R-UI-5) and the tags-to-review queue (R-UI-7).
func (s *Server) reviewRoutes(mux *http.ServeMux) {
	mux.Handle("GET /games/{id}/review", s.requireSession(http.HandlerFunc(s.reviewPage)))
	mux.Handle("POST /games/{id}/review", s.requireSession(http.HandlerFunc(s.reviewSave)))
	mux.Handle("POST /games/{id}/review/skip", s.requireSession(http.HandlerFunc(s.reviewSkip)))
	mux.Handle("GET /queue", s.requireSession(http.HandlerFunc(s.queuePage)))
}

func reviewHref(gameID int64, version string) string {
	h := "/games/" + strconv.FormatInt(gameID, 10) + "/review"
	if version != "" {
		h += "?version=" + url.QueryEscape(version)
	}
	return h
}

func reviewGameID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%w: game %q", domain.ErrNotFound, r.PathValue("id"))
	}
	return id, nil
}

func (s *Server) reviewPage(w http.ResponseWriter, r *http.Request) {
	id, err := reviewGameID(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	v, err := s.buildReview(r.Context(), id, r.URL.Query().Get("version"))
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabQueue, "Tag review"), ui.TagReviewPage(v)), "tag-list")
}

// reviewSave posts one "v-<gameTagId>" field per answered tag; unanswered tags are left alone.
func (s *Server) reviewSave(w http.ResponseWriter, r *http.Request) {
	id, err := reviewGameID(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.respondError(w, r, fmt.Errorf("%w: unreadable form", domain.ErrValidation))
		return
	}
	verdicts := map[int64]string{}
	for k, vals := range r.PostForm {
		raw, ok := strings.CutPrefix(k, "v-")
		if !ok || len(vals) == 0 {
			continue
		}
		tagID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			s.respondError(w, r, fmt.Errorf("%w: unknown tag field", domain.ErrValidation))
			return
		}
		verdicts[tagID] = vals[len(vals)-1]
	}
	if _, err := s.tags.SaveReview(r.Context(), id, verdicts); err != nil {
		s.respondError(w, r, err)
		return
	}
	reviewRedirect(w, r, "/games/"+strconv.FormatInt(id, 10))
}

func (s *Server) reviewSkip(w http.ResponseWriter, r *http.Request) {
	id, err := reviewGameID(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if err := s.tags.SkipReview(r.Context(), id); err != nil {
		s.respondError(w, r, err)
		return
	}
	reviewRedirect(w, r, "/queue")
}

// reviewRedirect sends htmx a client redirect and plain forms a 303.
func reviewRedirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) queuePage(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildQueue(r.Context())
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabQueue, "Tags to review"), ui.QueuePage(v)), "queue-list")
}
