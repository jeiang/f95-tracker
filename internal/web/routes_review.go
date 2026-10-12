package web

import (
	"context"
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
	mux.Handle("POST /games/{id}/review/synonym", s.requireSession(http.HandlerFunc(s.reviewSynonym)))
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
	v, err := s.buildReview(r.Context(), id, r.URL.Query().Get("version"), nil, "")
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
	verdicts, err := postedVerdicts(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if _, err := s.tags.SaveReview(r.Context(), id, verdicts); err != nil {
		s.respondError(w, r, err)
		return
	}
	to := "/games/" + strconv.FormatInt(id, 10)
	if r.PostForm.Get("next") != "" {
		if to, err = s.nextReviewHref(r.Context(), id); err != nil {
			s.respondError(w, r, err)
			return
		}
	}
	reviewRedirect(w, r, to)
}

// nextReviewHref is the review of the first queued Game other than gameID, or /queue.
func (s *Server) nextReviewHref(ctx context.Context, gameID int64) (string, error) {
	rows, err := s.tags.Queue(ctx)
	if err != nil {
		return "", err
	}
	for _, q := range rows {
		if q.GameID != gameID {
			return reviewHref(q.GameID, q.LastPlayedVersion.String), nil
		}
	}
	return "/queue", nil
}

// postedVerdicts reads the "v-<gameTagId>" fields of a parsed review form.
func postedVerdicts(r *http.Request) (map[int64]string, error) {
	verdicts := map[int64]string{}
	for k, vals := range r.PostForm {
		raw, ok := strings.CutPrefix(k, "v-")
		if !ok || len(vals) == 0 {
			continue
		}
		tagID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: unknown tag field", domain.ErrValidation)
		}
		verdicts[tagID] = vals[len(vals)-1]
	}
	return verdicts, nil
}

// reviewSynonym re-points a Genre-phrase tag (and saves the Synonym), as game
// detail's Map action does. htmx gets the tag list back with the unsaved
// verdicts of the posted form kept.
func (s *Server) reviewSynonym(w http.ResponseWriter, r *http.Request) {
	id, err := reviewGameID(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.respondError(w, r, fmt.Errorf("%w: unreadable form", domain.ErrValidation))
		return
	}
	posted, err := postedVerdicts(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	tagID, err := strconv.ParseInt(r.PostForm.Get("tag"), 10, 64)
	if err != nil {
		s.respondError(w, r, fmt.Errorf("%w: unknown tag", domain.ErrValidation))
		return
	}
	// Rows carry their own input name so several can live in one form.
	target := r.PostForm.Get("target-" + strconv.FormatInt(tagID, 10))
	if target == "" {
		target = r.PostForm.Get("target")
	}
	notice, err := s.applySynonym(r.Context(), id, tagID, target)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if !isHTMX(r) {
		http.Redirect(w, r, reviewHref(id, r.URL.Query().Get("version")), http.StatusSeeOther)
		return
	}
	v, err := s.buildReview(r.Context(), id, r.URL.Query().Get("version"), posted, notice)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabQueue, "Tag review"), ui.TagReviewPage(v)), "tag-list")
}

func (s *Server) applySynonym(ctx context.Context, gameID, tagID int64, target string) (string, error) {
	rows, err := s.tags.GameTags(ctx, gameID)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.ID != tagID {
			continue
		}
		if !canMapRow(row.SourcePhrase.Valid, row.Origin) {
			return "", fmt.Errorf("%w: only a tag from the Genre text can become a Synonym", domain.ErrValidation)
		}
		ref, err := s.tagRefOf(ctx, target)
		if err != nil {
			return "", err
		}
		res, err := s.tags.SetMapping(ctx, tagID, ref)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("“%s” is now a Synonym of %s; %d other Games updated", row.SourcePhrase.String, ref.Label, res.Repointed), nil
	}
	return "", fmt.Errorf("%w: game tag %d", domain.ErrNotFound, tagID)
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
