package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

const maxLinksForm = 256 << 10

const maskedMessage = "Masked F95 links (f95zone.to/masked/…) cannot be used. Open them in your browser, solve the captcha, and paste the final pixeldrain, mega or gofile links instead."

// downloadRoutes: the downloads list, the paste page and the Game detail Download actions (R-DL-1, R-DL-3).
func (s *Server) downloadRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /downloads", s.downloadsPage)
	h("GET /downloads/{jobId}/links", s.pasteLinksPage)
	h("POST /downloads/{jobId}/links", s.pasteLinks)
	h("POST /downloads/{jobId}/{action}", s.downloadAction)
	h("POST /games/{id}/download", s.gameDownload)
	h("POST /games/{id}/platform-pref", s.gamePlatformPref)
}

func (s *Server) renderDownloads(w http.ResponseWriter, r *http.Request, msg, fragment string) {
	v, err := s.buildDownloads(r.Context(), msg)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabDownloads, "Downloads"), ui.DownloadsPage(v)), fragment)
}

func (s *Server) downloadsPage(w http.ResponseWriter, r *http.Request) {
	s.renderDownloads(w, r, "", "")
}

var actionMessages = map[string]string{"cancel": "Job cancelled.", "done": "Job marked done.", "retry": "Job queued again."}

func (s *Server) downloadAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "jobId")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	action := r.PathValue("action")
	switch action {
	case "cancel":
		_, err = s.downloads.Cancel(r.Context(), id)
	case "done":
		_, err = s.downloads.MarkDone(r.Context(), id)
	case "retry":
		_, err = s.downloads.Retry(r.Context(), id)
	default:
		err = domain.ErrNotFound
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if isHTMX(r) {
		s.renderDownloads(w, r, actionMessages[action], "download-list")
		return
	}
	http.Redirect(w, r, "/downloads", http.StatusSeeOther)
}

func (s *Server) pasteLinksPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "jobId")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.renderPaste(w, r, http.StatusOK, id, "", "", "")
}

func (s *Server) renderPaste(w http.ResponseWriter, r *http.Request, status int, id int64, links, platform, errMsg string) {
	j, err := s.downloads.Get(r.Context(), id)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	page := ui.Layout(s.pageMeta(r, ui.TabDownloads, "Links for "+j.GameName), ui.PasteLinksPage(s.buildPasteLinks(j, links, platform, errMsg)))
	s.renderStatus(w, r, status, page, "")
}

func (s *Server) pasteLinks(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "jobId")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLinksForm)
	if err := r.ParseForm(); err != nil {
		s.respondError(w, r, errors.Join(domain.ErrValidation, err))
		return
	}
	links, platform := r.FormValue("links"), r.FormValue("platform")
	_, err = s.downloads.IngestLinks(r.Context(), id, parseLinkLines(links, platform))
	switch {
	case err == nil:
		http.Redirect(w, r, "/downloads", http.StatusSeeOther)
	case errors.Is(err, downloads.ErrMasked):
		s.renderPaste(w, r, http.StatusUnprocessableEntity, id, links, platform, maskedMessage)
	case errors.Is(err, domain.ErrValidation):
		s.renderPaste(w, r, http.StatusUnprocessableEntity, id, links, platform, err.Error())
	default:
		s.respondError(w, r, err)
	}
}

// gameDownload is the Download button: create or open the job for the latest version and go to
// its paste page when it waits for links, else to the list.
func (s *Server) gameDownload(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	job, _, err := s.downloads.Create(r.Context(), id)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	to := "/downloads"
	if job.State == downloads.AwaitingLinks {
		to = "/downloads/" + strconv.FormatInt(job.ID, 10) + "/links"
	}
	redirectTo(w, r, to)
}

func (s *Server) gamePlatformPref(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		var pref *domain.PlatformPref
		if v := domain.PlatformPref(r.FormValue("platform_pref")); v != "" {
			pref = &v
		}
		err = s.games.SetPlatformPref(r.Context(), id, pref)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.done(w, r, id, "game-detail")
}
