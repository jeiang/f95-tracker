package web

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// settingsState carries what the request just did into the view: the outcome
// messages, and the synonym filter.
type settingsState struct {
	cookieMsg, cookieTone string
	alertSaved            bool
	synFilter, synMsg     string
	ntfyTested            bool
	ntfyErr               string
}

func (s *Server) buildSettings(ctx context.Context, st settingsState) (ui.SettingsView, error) {
	var v ui.SettingsView
	var err error
	if v.Cookie, err = s.cookieView(ctx, st); err != nil {
		return v, err
	}
	if v.Alert, err = s.alertView(ctx, st.alertSaved); err != nil {
		return v, err
	}
	if v.Synonyms, err = s.synonymsView(ctx, st); err != nil {
		return v, err
	}
	v.Ntfy = ui.NtfyView{Enabled: s.notify != nil && s.notify.Enabled(), URL: s.cfg.NtfyURL, Topic: s.cfg.NtfyTopic,
		Tested: st.ntfyTested, Err: st.ntfyErr}
	return v, nil
}

func (s *Server) cookieView(ctx context.Context, st settingsState) (ui.CookieView, error) {
	v := ui.CookieView{Message: st.cookieMsg, MessageTone: st.cookieTone}
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
	v := ui.SynonymsView{Filter: st.synFilter, Message: st.synMsg}
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
