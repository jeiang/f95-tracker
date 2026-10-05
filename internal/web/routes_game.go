package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// gameRoutes: the Game detail page, its actions and the cover files.
func (s *Server) gameRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /games/{id}", s.gameDetail)
	h("POST /games/{id}/refresh", s.gameRefresh)
	h("POST /games/{id}/play-status", s.gamePlayStatus)
	h("POST /games/{id}/rating", s.gameRating)
	h("POST /games/{id}/play-log", s.playLogAdd)
	h("POST /games/{id}/play-log/{entryId}", s.playLogEdit)
	h("POST /games/{id}/play-log/{entryId}/delete", s.playLogDelete)
	h("POST /games/{id}/tags", s.gameTagAdd)
	h("POST /games/{id}/tags/{tagId}", s.gameTagEdit)
	h("POST /games/{id}/sources", s.sourceAdd)
	h("POST /games/{id}/sources/{sid}", s.sourceEdit)
	h("POST /games/{id}/delete", s.gameDelete)
	h("GET /covers/{gameId}", s.cover)
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: %s %q", domain.ErrNotFound, name, r.PathValue(name))
	}
	return id, nil
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// detailPage renders the Game page (or one of its fragments for htmx).
func (s *Server) detailPage(w http.ResponseWriter, r *http.Request, status int, id int64, note detailNote, fragment string) {
	v, err := s.buildGameDetail(r.Context(), id, note)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	page := ui.Layout(s.pageMeta(r, ui.TabGames, v.Name), ui.GameDetailPage(v))
	s.renderStatus(w, r, status, page, fragment)
}

// done answers a successful action: the re-rendered fragment for htmx, else
// redirect back to the Game (R-UI-12).
func (s *Server) done(w http.ResponseWriter, r *http.Request, id int64, fragment string) {
	if isHTMX(r) {
		s.detailPage(w, r, http.StatusOK, id, detailNote{}, fragment)
		return
	}
	http.Redirect(w, r, "/games/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func redirectTo(w http.ResponseWriter, r *http.Request, to string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", to)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) gameDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.detailPage(w, r, http.StatusOK, id, detailNote{}, "")
}

// refreshFailure maps a Refresh error to a short inline message and status.
func refreshFailure(err error) (string, int) {
	switch {
	case errors.Is(err, f95.ErrBusy):
		return "F95 busy, try again.", http.StatusServiceUnavailable
	case errors.Is(err, f95.ErrCookieInvalid):
		return "The F95 cookie is no longer valid. Update it in Settings.", http.StatusBadGateway
	case errors.Is(err, f95.ErrBlocked), errors.Is(err, itch.ErrBlocked):
		return "The Source is rate limiting or blocking requests. Try again later.", http.StatusBadGateway
	case errors.Is(err, f95.ErrRestricted):
		return "The thread is restricted or unavailable.", http.StatusBadGateway
	case errors.Is(err, itch.ErrNotFound):
		return "The page was not found at itch.io.", http.StatusBadGateway
	case errors.Is(err, f95.ErrParse):
		return "The Source answered in an unexpected format.", http.StatusBadGateway
	}
	return "", 0
}

func (s *Server) gameRefresh(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	d, err := s.games.Detail(r.Context(), id)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	runID, finish, err := s.refresher.StartManualRun(r.Context())
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	res, err := s.refresher.Refresh(r.Context(), d.Primary.ID, runID)
	if err != nil {
		finish("failed")
		msg, status := refreshFailure(err)
		if status == 0 {
			s.respondError(w, r, err)
			return
		}
		s.detailPage(w, r, status, id, detailNote{msg: msg, failed: true}, "game-detail")
		return
	}
	finish("ok")
	s.detailPage(w, r, http.StatusOK, id, detailNote{msg: refreshSummary(res)}, "game-detail")
}

func refreshSummary(res check.RefreshResult) string {
	parts := []string{"Checked just now"}
	switch {
	case res.Update():
		parts = append(parts, "Update found")
	case res.NotTrackable:
		parts = append(parts, "not trackable, update by hand")
	case res.Outcome == check.OutcomeUnchanged:
		parts = append(parts, "no change")
	}
	if n := res.Tags.New; n > 0 {
		parts = append(parts, fmt.Sprintf("%d tag(s) added at Source (marked new)", n))
	}
	if n := res.Tags.Removed; n > 0 {
		parts = append(parts, fmt.Sprintf("%d tag(s) removed at Source", n))
	}
	return strings.Join(parts, " · ")
}

func (s *Server) gamePlayStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		err = s.games.SetPlayStatus(r.Context(), id, domain.PlayStatus(r.FormValue("play_status")))
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}

func (s *Server) gameRating(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		var x2 int
		if x2, err = strconv.Atoi(r.FormValue("rating_x2")); err != nil {
			err = fmt.Errorf("%w: rating must be a number", domain.ErrValidation)
		} else if x2 == 0 {
			err = s.games.SetRating(r.Context(), id, nil)
		} else {
			stars := float64(x2) / 2
			err = s.games.SetRating(r.Context(), id, &stars)
		}
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}

func (s *Server) playLogAdd(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	ctx := r.Context()
	added, err := s.games.AddPlayLog(ctx, id, r.FormValue("version"), r.FormValue("played_on"))
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	due, _, err := s.tags.ReviewDue(ctx, id)
	if err == nil && due {
		err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return s.tags.EnsurePending(ctx, q, id) })
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if due {
		redirectTo(w, r, fmt.Sprintf("/games/%d/review?version=%s", id, url.QueryEscape(added.Entry.Version)))
		return
	}
	s.done(w, r, id, "game-detail")
}

// entryOf loads a Play log entry and checks it belongs to the Game.
func (s *Server) entryOf(r *http.Request) (gameID, entryID int64, err error) {
	if gameID, err = pathID(r, "id"); err != nil {
		return
	}
	if entryID, err = pathID(r, "entryId"); err != nil {
		return
	}
	e, err := s.store.Queries().GetPlayLog(r.Context(), entryID)
	if err != nil || e.GameID != gameID {
		return 0, 0, fmt.Errorf("%w: play log entry %d", domain.ErrNotFound, entryID)
	}
	return gameID, entryID, nil
}

func (s *Server) playLogEdit(w http.ResponseWriter, r *http.Request) {
	id, eid, err := s.entryOf(r)
	if err == nil {
		_, err = s.games.EditPlayLog(r.Context(), eid, r.FormValue("version"), r.FormValue("played_on"))
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}

func (s *Server) playLogDelete(w http.ResponseWriter, r *http.Request) {
	id, eid, err := s.entryOf(r)
	if err == nil {
		err = s.games.DeletePlayLog(r.Context(), eid)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}

// tagRefOf resolves typed text to a known tag (by label or slug) or a Custom tag.
func (s *Server) tagRefOf(ctx context.Context, text string) (tags.TagRef, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return tags.TagRef{}, fmt.Errorf("%w: tag name is required", domain.ErrValidation)
	}
	known, err := s.tags.Tags(ctx)
	if err != nil {
		return tags.TagRef{}, err
	}
	for _, t := range known {
		if t.Kind == tags.KindF95 && (strings.EqualFold(t.Label, text) || strings.EqualFold(t.Slug, text)) {
			return t, nil
		}
	}
	for _, t := range known {
		if strings.EqualFold(t.Label, text) {
			return t, nil
		}
	}
	return tags.TagRef{Kind: tags.KindCustom, Label: text}, nil
}

func (s *Server) gameTagAdd(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		var ref tags.TagRef
		if ref, err = s.tagRefOf(r.Context(), r.FormValue("tag")); err == nil {
			_, err = s.tags.AddByHand(r.Context(), id, ref, r.FormValue("qualifier"))
		}
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "tag-list")
}

func (s *Server) gameTagEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	var tid int64
	if err == nil {
		tid, err = pathID(r, "tagId")
	}
	if err == nil {
		err = s.editGameTag(r, id, tid)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "tag-list")
}

func (s *Server) editGameTag(r *http.Request, gameID, tagID int64) error {
	ctx := r.Context()
	rows, err := s.tags.GameTags(ctx, gameID)
	if err != nil {
		return err
	}
	found := false
	for _, row := range rows {
		found = found || row.ID == tagID
	}
	if !found {
		return fmt.Errorf("%w: game tag %d", domain.ErrNotFound, tagID)
	}
	switch {
	case r.FormValue("verification") != "":
		return s.tags.SetVerification(ctx, tagID, r.FormValue("verification"))
	case r.FormValue("qualifier") != "":
		return s.tags.SetQualifier(ctx, tagID, r.FormValue("qualifier"))
	case r.FormValue("mapping") != "":
		ref, err := s.tagRefOf(ctx, r.FormValue("mapping"))
		if err != nil {
			return err
		}
		_, err = s.tags.SetMapping(ctx, tagID, ref)
		return err
	case r.FormValue("dismiss") != "":
		return s.tags.DismissRemoved(ctx, tagID)
	}
	return fmt.Errorf("%w: nothing to change", domain.ErrValidation)
}

var f95ThreadRe = regexp.MustCompile(`^/threads/(?:[^/]*\.)?(\d+)/?`)

// linkSourceSpec classifies a pasted URL as an F95 thread, an itch.io page or a manual link.
func linkSourceSpec(raw string) (games.SourceSpec, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return games.SourceSpec{}, fmt.Errorf("%w: %q is not a web link", domain.ErrValidation, raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "f95zone.to" || host == "www.f95zone.to" {
		if m := f95ThreadRe.FindStringSubmatch(u.Path); m != nil {
			return games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: m[1], URL: "https://f95zone.to/threads/" + m[1] + "/"}, nil
		}
	}
	if slug := strings.Trim(u.Path, "/"); strings.HasSuffix(host, ".itch.io") && slug != "" && !strings.Contains(slug, "/") {
		return games.SourceSpec{Kind: domain.SourceItchio, ExternalID: host + "/" + strings.ToLower(slug), URL: "https://" + host + "/" + slug}, nil
	}
	return games.SourceSpec{Kind: domain.SourceManual, URL: u.String()}, nil
}

func (s *Server) sourceAdd(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		var sp games.SourceSpec
		if sp, err = linkSourceSpec(r.FormValue("url")); err == nil {
			_, err = s.games.AddLinkSource(r.Context(), id, sp)
		}
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}

func (s *Server) sourceEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	var sid int64
	if err == nil {
		sid, err = pathID(r, "sid")
	}
	if err == nil {
		err = s.editSource(r, id, sid)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}

func (s *Server) editSource(r *http.Request, gameID, sid int64) error {
	ctx := r.Context()
	src, err := s.store.Queries().GetSource(ctx, sid)
	if err != nil || src.GameID != gameID {
		return fmt.Errorf("%w: source %d", domain.ErrNotFound, sid)
	}
	primaryOnly := func() error {
		if src.IsPrimary != 1 {
			return fmt.Errorf("%w: only the primary Source can be changed this way", domain.ErrValidation)
		}
		return nil
	}
	switch r.FormValue("action") {
	case "primary":
		return s.games.SetPrimary(ctx, sid)
	case "dev_status":
		if err := primaryOnly(); err != nil {
			return err
		}
		var ds *domain.DevStatus
		if v := r.FormValue("dev_status"); v != "" {
			d := domain.DevStatus(v)
			ds = &d
		}
		return s.games.SetDevStatus(ctx, gameID, ds)
	case "reenable":
		if err := primaryOnly(); err != nil {
			return err
		}
		return s.games.ReenableChecks(ctx, gameID)
	}
	return fmt.Errorf("%w: unknown source action", domain.ErrValidation)
}

func (s *Server) gameDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil && r.FormValue("confirm") != "yes" {
		err = fmt.Errorf("%w: confirm removing the Game", domain.ErrValidation)
	}
	if err == nil {
		err = s.games.Remove(r.Context(), id)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	redirectTo(w, r, "/games")
}

func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "gameId")
	var path string
	if err == nil {
		path, err = s.games.CoverPath(r.Context(), id)
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		s.log.Error("cover", "game_id", id, "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
	default:
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, path)
	}
}
