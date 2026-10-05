package web

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// settingsState carries what the request just did into the view. code is a
// fixed result code (also carried through the no-JS redirect as ?r=), n and c
// its counts; no free text travels in a URL.
type settingsState struct {
	code      string
	n, c      int
	synFilter string
	ntfyErr   string // htmx only: the delivery error
}

var cookieResults = map[string][2]string{
	"cookie-ok":      {"Cookie saved and logged in to F95.", "ok"},
	"cookie-invalid": {"F95 did not accept this cookie. It is saved but marked invalid; paste a fresh one.", "bad"},
	"cookie-busy":    {"F95 busy, try again.", "warn"},
	"cookie-blocked": {"F95 is blocking requests right now. The cookie was saved; try the check again later.", "warn"},
	"cookie-failed":  {"Could not reach F95. The cookie was saved; try the check again.", "warn"},
}

func (st settingsState) query() url.Values {
	q := url.Values{"r": {st.code}}
	if st.n > 0 {
		q.Set("n", strconv.Itoa(st.n))
	}
	if st.c > 0 {
		q.Set("c", strconv.Itoa(st.c))
	}
	return q
}

func decodeState(q url.Values) settingsState {
	st := settingsState{synFilter: strings.TrimSpace(q.Get("syn")), code: q.Get("r")}
	st.n, _ = strconv.Atoi(q.Get("n"))
	st.c, _ = strconv.Atoi(q.Get("c"))
	return st
}

func (st settingsState) synMessage() string {
	res := tags.ReapplyResult{Repointed: st.n, Collapsed: st.c}
	switch st.code {
	case "syn-added":
		return reapplyMessage("added", res)
	case "syn-updated":
		return reapplyMessage("updated", res)
	case "syn-removed":
		return "Synonym removed. Existing Game tags are unchanged."
	}
	return ""
}

func (s *Server) buildSettings(ctx context.Context, st settingsState) (ui.SettingsView, error) {
	var v ui.SettingsView
	var err error
	if v.Cookie, err = s.cookieView(ctx, st); err != nil {
		return v, err
	}
	if v.Alert, err = s.alertView(ctx, st.code == "alert-saved"); err != nil {
		return v, err
	}
	if v.Synonyms, err = s.synonymsView(ctx, st); err != nil {
		return v, err
	}
	v.Ntfy = ui.NtfyView{Enabled: s.notify != nil && s.notify.Enabled(), URL: s.cfg.NtfyURL, Topic: s.cfg.NtfyTopic,
		Tested: st.code == "ntfy-ok" || st.code == "ntfy-failed", Err: st.ntfyErr}
	if st.code == "ntfy-failed" && v.Ntfy.Err == "" {
		v.Ntfy.Err = "ntfy did not accept the test message."
	}
	return v, nil
}

func (s *Server) cookieView(ctx context.Context, st settingsState) (ui.CookieView, error) {
	v := ui.CookieView{}
	v.Message, v.MessageTone = cookieResults[st.code][0], cookieResults[st.code][1]
	row, err := f95.NewCredStore(s.store, s.clock).Get(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Stored, v.Validity, v.TfaExpires = true, row.Validity, row.TfaTrustExpiresAt.String
	if row.ValidatedAt.Valid {
		if at, err := clock.ParseTimestamp(row.ValidatedAt.String); err == nil {
			v.CheckedAt = at.Format("2006-01-02 15:04") + " UTC"
		}
	}
	return v, nil
}

func (s *Server) alertView(ctx context.Context, saved bool) (ui.AlertView, error) {
	counts, err := s.games.PlayStatusCounts(ctx)
	if err != nil {
		return ui.AlertView{}, err
	}
	set, err := s.games.AlertSet(ctx)
	if err != nil {
		return ui.AlertView{}, err
	}
	in := make(map[string]bool, len(set))
	for _, ps := range set {
		in[string(ps)] = true
	}
	v := ui.AlertView{Saved: saved}
	for _, ps := range ui.PlayStatuses {
		r := ui.AlertRow{Status: ps, Count: counts[ps], Checked: in[string(ps)]}
		v.Total += r.Count
		if r.Checked {
			v.Affected += r.Count
		}
		v.Rows = append(v.Rows, r)
	}
	return v, nil
}

func (s *Server) synonymsView(ctx context.Context, st settingsState) (ui.SynonymsView, error) {
	v := ui.SynonymsView{Filter: st.synFilter, Message: st.synMessage()}
	rows, err := s.tags.ListSynonyms(ctx, st.synFilter)
	if err != nil {
		return v, err
	}
	for _, r := range rows {
		target := r.Tag.Slug
		if r.Tag.Kind == tags.KindCustom {
			target = r.Tag.Label
		}
		v.Rows = append(v.Rows, ui.SynonymRow{ID: r.ID, Phrase: r.PhraseKey, Target: target, Kind: r.Tag.Kind})
	}
	all, err := s.tags.Tags(ctx)
	if err != nil {
		return v, err
	}
	for _, t := range all {
		if t.Kind == tags.KindCustom {
			v.Options = append(v.Options, ui.SynonymOption{Value: t.Label, Label: "Custom tag"})
		} else {
			v.Options = append(v.Options, ui.SynonymOption{Value: t.Slug, Label: t.Label})
		}
	}
	return v, nil
}
