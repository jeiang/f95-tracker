package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

const maxSettingsForm = 64 << 10

// settingsRoutes: F95 cookie, alert set, Synonym editor, ntfy test (R-SET-1..4).
func (s *Server) settingsRoutes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.Handler { return s.requireSession(f) }
	mux.Handle("GET /settings", h(s.settingsPage))
	mux.Handle("POST /settings/cookie", h(s.saveCookie))
	mux.Handle("POST /settings/alert-set", h(s.saveAlertSet))
	mux.Handle("POST /settings/synonyms", h(s.createSynonym))
	mux.Handle("POST /settings/synonyms/{id}", h(s.updateSynonym))
	mux.Handle("POST /settings/synonyms/{id}/delete", h(s.deleteSynonym))
	mux.Handle("DELETE /settings/synonyms/{id}", h(s.deleteSynonym))
	mux.Handle("POST /settings/ntfy/test", h(s.ntfyTest))
	mux.Handle("POST /settings/downloads", h(s.saveDownloadPrefs))
	mux.Handle("POST /settings/tokens", h(s.createToken))
	mux.Handle("POST /settings/tokens/{id}/revoke", h(s.revokeToken))
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, st settingsState, fragment string) {
	v, err := s.buildSettings(r.Context(), st)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if st.token != nil {
		w.Header().Set("Cache-Control", "no-store") // the page carries a token shown only once
	}
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabSettings, "Settings"), ui.SettingsPage(v)), fragment)
}

// done answers a successful POST: the fragment for htmx, otherwise a 303 back to
// the page with the result code (R-UI-12).
func (s *Server) settingsDone(w http.ResponseWriter, r *http.Request, st settingsState, fragment, anchor string) {
	if r.Header.Get("HX-Request") == "true" {
		s.renderSettings(w, r, st, fragment)
		return
	}
	http.Redirect(w, r, "/settings?"+st.query().Encode()+"#"+anchor, http.StatusSeeOther)
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	st := decodeState(r.URL.Query())
	frag := ""
	if r.Header.Get("HX-Target") == "synonym-list" {
		frag = "synonym-list"
	}
	s.renderSettings(w, r, st, frag)
}

func (s *Server) parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxSettingsForm)
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("%w: unreadable form", domain.ErrValidation)
	}
	return nil
}

// saveCookie stores the pasted Cookie header and validates it with one thread load.
// The header is never echoed, logged or put in an error message.
func (s *Server) saveCookie(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.respondError(w, r, err)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("cookie"))
	if raw == "" {
		s.respondError(w, r, fmt.Errorf("%w: paste the Cookie header first", domain.ErrValidation))
		return
	}
	var expires time.Time
	if d := strings.TrimSpace(r.PostFormValue("tfa_expires")); d != "" {
		t, err := clock.ParseDate(d)
		if err != nil {
			s.respondError(w, r, fmt.Errorf("%w: two-step trust expiry must be a date (YYYY-MM-DD)", domain.ErrValidation))
			return
		}
		expires = t
	}
	creds := f95.NewCredStore(s.store, s.clock)
	threadID, err := creds.ProbeThread(r.Context())
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	st := settingsState{}
	ok, err := s.f95.SaveAndValidate(r.Context(), raw, r.UserAgent(), expires, threadID)
	switch {
	case errors.Is(err, domain.ErrValidation):
		s.respondError(w, r, err)
		return
	case errors.Is(err, f95.ErrBusy):
		st.code = "cookie-busy"
	case errors.Is(err, f95.ErrBlocked):
		st.code = "cookie-blocked"
	case err != nil:
		s.log.Error("cookie check", "err", err)
		st.code = "cookie-failed"
	case ok:
		st.code = "cookie-ok"
	default:
		st.code = "cookie-invalid"
	}
	s.settingsDone(w, r, st, "cookie-status", "cookie")
}

func (s *Server) saveAlertSet(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.respondError(w, r, err)
		return
	}
	var set []domain.PlayStatus
	for _, v := range r.PostForm["status"] {
		set = append(set, domain.PlayStatus(v))
	}
	if err := s.games.SetAlertSet(r.Context(), set); err != nil {
		s.respondError(w, r, err)
		return
	}
	s.settingsDone(w, r, settingsState{code: "alert-saved"}, "alert-set", "alerts")
}

// synonymTarget resolves the form's target text and kind to a tag. An F95 target
// must already be in the vocabulary (slug or label), so a typo cannot mint a tag.
func (s *Server) synonymTarget(r *http.Request) (tags.TagRef, error) {
	target := strings.TrimSpace(r.PostFormValue("target"))
	if target == "" {
		return tags.TagRef{}, fmt.Errorf("%w: choose the tag this phrase maps to", domain.ErrValidation)
	}
	if r.PostFormValue("kind") == tags.KindCustom {
		return tags.TagRef{Kind: tags.KindCustom, Label: target}, nil
	}
	all, err := s.tags.Tags(r.Context())
	if err != nil {
		return tags.TagRef{}, err
	}
	for _, t := range all {
		if t.Kind == tags.KindF95 && (strings.EqualFold(t.Slug, target) || strings.EqualFold(t.Label, target)) {
			return t, nil
		}
	}
	return tags.TagRef{}, fmt.Errorf("%w: no F95 tag %q; pick one from the list or choose Custom tag", domain.ErrValidation, target)
}

func reapplyMessage(verb string, res tags.ReapplyResult) string {
	msg := fmt.Sprintf("Synonym %s. Re-pointed %d Game %s", verb, res.Repointed, plural(res.Repointed, "tag", "tags"))
	if res.Collapsed > 0 {
		msg += fmt.Sprintf(" (%d merged into a tag the Game already had)", res.Collapsed)
	}
	return msg + "."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Server) createSynonym(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.respondError(w, r, err)
		return
	}
	target, err := s.synonymTarget(r)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	_, res, err := s.tags.CreateSynonym(r.Context(), r.PostFormValue("phrase"), target)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.settingsDone(w, r, settingsState{code: "syn-added", n: res.Repointed, c: res.Collapsed}, "synonym-list", "synonyms")
}

func (s *Server) updateSynonym(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		err = s.parseForm(w, r)
	}
	var target tags.TagRef
	if err == nil {
		target, err = s.synonymTarget(r)
	}
	var res tags.ReapplyResult
	if err == nil {
		_, res, err = s.tags.UpdateSynonym(r.Context(), id, r.PostFormValue("phrase"), target)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.settingsDone(w, r, settingsState{code: "syn-updated", n: res.Repointed, c: res.Collapsed}, "synonym-list", "synonyms")
}

func (s *Server) deleteSynonym(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		err = s.tags.DeleteSynonym(r.Context(), id)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.settingsDone(w, r, settingsState{code: "syn-removed"}, "synonym-list", "synonyms")
}

func (s *Server) ntfyTest(w http.ResponseWriter, r *http.Request) {
	st := settingsState{code: "ntfy-ok"}
	if s.notify == nil {
		st.code, st.ntfyErr = "ntfy-failed", "ntfy is not configured."
	} else if err := s.notify.Test(r.Context()); err != nil {
		s.log.Warn("ntfy test failed", "err", err)
		st.code, st.ntfyErr = "ntfy-failed", err.Error()
	}
	s.settingsDone(w, r, st, "ntfy-status", "ntfy")
}

// saveDownloadPrefs stores the mirror host order and default platform (R-SET-5).
func (s *Server) saveDownloadPrefs(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.respondError(w, r, err)
		return
	}
	var order []string
	for _, h := range strings.Split(r.PostFormValue("host_order"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			if !slices.Contains(downloads.SupportedHosts, h) {
				s.respondError(w, r, fmt.Errorf("%w: %q is not a supported host (pixeldrain, mega, gofile)", domain.ErrValidation, h))
				return
			}
			order = append(order, h)
		}
	}
	err := s.games.SetSettings(r.Context(), games.Settings{PlatformPref: domain.PlatformPref(r.PostFormValue("platform_pref")), MirrorHostOrder: order})
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.settingsDone(w, r, settingsState{code: "downloads-saved"}, "download-prefs", "downloads")
}

// createToken issues an API token and renders the page that shows its plaintext once. It is a
// direct response, not a redirect: the plaintext must not travel in a URL or be reloadable.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.respondError(w, r, err)
		return
	}
	plain, tok, err := s.tokens.Create(r.Context(), r.PostFormValue("name"), auth.Scope(r.PostFormValue("scope")))
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.renderSettings(w, r, settingsState{token: &ui.NewToken{Name: tok.Name, Plaintext: plain}}, "tokens")
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err == nil {
		err = s.tokens.Revoke(r.Context(), id)
	}
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.settingsDone(w, r, settingsState{code: "token-revoked"}, "tokens", "tokens")
}
