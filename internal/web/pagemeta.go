package web

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

const tfaWarnWindow = 14 * 24 * time.Hour // R-F95-12

// pageMeta is the Layout input every page uses: the active tab, the global banners
// (R-UI-11) and whether the Import review tab shows. A failed lookup degrades to
// "no banner" rather than breaking the page.
func (s *Server) pageMeta(r *http.Request, tab ui.Tab, title string) ui.PageMeta {
	ctx := r.Context()
	q := s.store.Queries()
	meta := ui.PageMeta{Title: title, ActiveTab: tab}

	cred, err := q.GetF95CredentialHealth(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		s.log.Error("banner: f95 credential", "err", err)
	default:
		if cred.Validity == "invalid" {
			meta.Banners = append(meta.Banners, ui.Banner{
				Tone: "bad", Text: "The F95 cookie is no longer valid, so detail fetches are paused.",
				LinkText: "Update the cookie", LinkHref: "/settings",
			})
		}
		if b, ok := s.tfaBanner(cred.TfaTrustExpiresAt); ok {
			meta.Banners = append(meta.Banners, b)
		}
	}

	switch run, err := q.GetLatestCheckRun(ctx); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		s.log.Error("banner: latest check run", "err", err)
	case run.Status == "failed":
		meta.Banners = append(meta.Banners, ui.Banner{Tone: "warn", Text: "The last Source check failed."})
	case run.Status == "partial":
		meta.Banners = append(meta.Banners, ui.Banner{Tone: "warn", Text: "The last Source check finished with errors."})
	}

	n, err := q.CountImportReviewGames(ctx)
	if err != nil {
		s.log.Error("tab: import review count", "err", err)
	}
	meta.ShowImportReview = n > 0
	return meta
}

func (s *Server) tfaBanner(expires sql.NullString) (ui.Banner, bool) {
	if !expires.Valid {
		return ui.Banner{}, false
	}
	at, err := clock.ParseTimestamp(expires.String)
	if err != nil {
		if at, err = clock.ParseDate(expires.String); err != nil {
			return ui.Banner{}, false
		}
	}
	now := s.clock.Now()
	if at.Sub(now) > tfaWarnWindow {
		return ui.Banner{}, false
	}
	text := "The F95 two-step trust cookie expires on " + clock.Date(at) + "."
	if !at.After(now) {
		text = "The F95 two-step trust cookie expired on " + clock.Date(at) + "."
	}
	return ui.Banner{Tone: "warn", Text: text, LinkText: "Refresh the cookie", LinkHref: "/settings"}, true
}
