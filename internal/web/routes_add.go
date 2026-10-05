package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/genre"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// addRoutes: Add Game (R-UI-6): the form, the fetch + parse check fragment, and Confirm.
func (s *Server) addRoutes(mux *http.ServeMux) {
	mux.Handle("GET /games/new", s.requireSession(http.HandlerFunc(s.addForm)))
	mux.Handle("POST /games/fetch", s.requireSession(http.HandlerFunc(s.addFetch)))
	mux.Handle("POST /games", s.requireSession(http.HandlerFunc(s.addConfirm)))
}

const addFragment = "parse-panels"

func (s *Server) addForm(w http.ResponseWriter, r *http.Request) {
	s.renderAdd(w, r, http.StatusOK, ui.AddView{Kind: string(domain.SourceF95Thread)})
}

func (s *Server) renderAdd(w http.ResponseWriter, r *http.Request, status int, v ui.AddView) {
	if v.Parse != nil {
		if labels, err := s.vocabLabels(r.Context()); err != nil {
			s.log.Error("add: vocabulary", "err", err)
		} else {
			v.Vocab = labels
		}
	}
	s.renderStatus(w, r, status, ui.Layout(s.pageMeta(r, ui.TabAdd, "Add Game"), ui.AddGamePage(v)), addFragment)
}

func (s *Server) vocabLabels(ctx context.Context) ([]string, error) {
	all, err := s.tags.Tags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(all))
	for i, t := range all {
		out[i] = t.Label
	}
	return out, nil
}

var threadIDRe = regexp.MustCompile(`^\s*(?:https?://[^/\s]*/threads/(?:[^/\s]*\.)?)?(\d+)/?(?:[?#]\S*)?\s*$`)

// f95ThreadID extracts the thread id from a thread URL or a bare id.
func f95ThreadID(in string) (string, bool) {
	m := threadIDRe.FindStringSubmatch(in)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func f95ThreadURL(id string) string { return f95.DefaultBaseURL + "/threads/" + id + "/" }

// itchSourceSpec normalises an itch.io game link (itch.CanonicalURL).
func itchSourceSpec(raw string) (games.SourceSpec, error) {
	id, u, err := itch.CanonicalURL(raw)
	if err != nil {
		return games.SourceSpec{}, fmt.Errorf("%w: that does not look like an itch.io game page link", domain.ErrValidation)
	}
	return games.SourceSpec{Kind: domain.SourceItchio, ExternalID: id, URL: u}, nil
}

func manualSourceSpec(raw string) (games.SourceSpec, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return games.SourceSpec{}, fmt.Errorf("%w: enter a full http(s) link", domain.ErrValidation)
	}
	return games.SourceSpec{Kind: domain.SourceManual, URL: u.String()}, nil
}

// trackedBy returns the Game that already tracks sp, if any.
func (s *Server) trackedBy(ctx context.Context, sp games.SourceSpec) (*ui.AddTracked, error) {
	q := s.store.Queries()
	var src sqlcgen.Source
	var err error
	if sp.ExternalID != "" {
		src, err = q.FindSourceByExternal(ctx, sqlcgen.FindSourceByExternalParams{Kind: string(sp.Kind), ExternalID: sql.NullString{String: sp.ExternalID, Valid: true}})
	} else {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) && sp.Kind != domain.SourceF95Thread {
		src, err = q.FindSourceByURL(ctx, sqlcgen.FindSourceByURLParams{Kind: string(sp.Kind), Url: sp.URL})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g, err := q.GetGame(ctx, src.GameID)
	if err != nil {
		return nil, err
	}
	return &ui.AddTracked{GameID: g.ID, Name: g.Name}, nil
}

// fetched is everything the fetch step learned about a new Source.
type fetched struct {
	spec          games.SourceSpec
	name          string
	version       string
	changeKey     string
	devStatus     string
	threadUpdated string
	coverURL      string
	pending       bool
	gotDetail     bool // a page was read, so the add counts as a detail fetch
	genreText     string
	f95Tags       []f95.Tag
}

func (f fetched) hidden() [][2]string {
	tagsJSON, _ := json.Marshal(f.f95Tags)
	return [][2]string{
		{"kind", string(f.spec.Kind)}, {"url", f.spec.URL}, {"ext", f.spec.ExternalID},
		{"version", f.version}, {"change_key", f.changeKey}, {"dev_status", f.devStatus},
		{"thread_updated", f.threadUpdated}, {"cover_url", f.coverURL},
		{"pending", strconv.FormatBool(f.pending)}, {"fetched", strconv.FormatBool(f.gotDetail)},
		{"genre_text", f.genreText}, {"f95_tags", string(tagsJSON)},
	}
}

func (s *Server) addFetch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.respondError(w, r, fmt.Errorf("%w: %v", domain.ErrValidation, err))
		return
	}
	v := ui.AddView{
		Kind: r.PostForm.Get("kind"), Input: strings.TrimSpace(r.PostForm.Get("input")),
		Name: strings.TrimSpace(r.PostForm.Get("name")), Version: strings.TrimSpace(r.PostForm.Get("version")),
		TagText: r.PostForm.Get("tags"),
	}
	if !domain.SourceKind(v.Kind).Valid() {
		v.Kind = string(domain.SourceF95Thread)
	}
	invalid := func(msg string) {
		v.Error = msg
		s.renderAdd(w, r, http.StatusUnprocessableEntity, v)
	}
	ctx := r.Context()

	var f fetched
	switch domain.SourceKind(v.Kind) {
	case domain.SourceF95Thread:
		id, ok := f95ThreadID(v.Input)
		if !ok {
			invalid("That does not look like an F95zone thread link or thread id.")
			return
		}
		f.spec = games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: id, URL: f95ThreadURL(id)}
	case domain.SourceItchio:
		spec, err := itchSourceSpec(v.Input)
		if err != nil {
			invalid("That does not look like an itch.io game page link.")
			return
		}
		f.spec = spec
	default:
		spec, err := manualSourceSpec(v.Input)
		if err != nil {
			invalid("Enter a full link starting with http:// or https://.")
			return
		}
		if v.Name == "" {
			invalid("Give the Game a name.")
			return
		}
		f.spec, f.name, f.version = spec, v.Name, v.Version
	}

	tracked, err := s.trackedBy(ctx, f.spec)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	if tracked != nil {
		v.Tracked = tracked
		s.renderAdd(w, r, http.StatusConflict, v)
		return
	}

	switch f.spec.Kind {
	case domain.SourceF95Thread:
		if !s.fetchF95(w, r, v, &f) {
			return
		}
	case domain.SourceItchio:
		if !s.fetchItch(w, r, v, &f) {
			return
		}
		f.genreText = v.TagText
	default:
		f.genreText = v.TagText
	}

	model, err := s.tags.ParseCheck(ctx, f.genreText, f.f95Tags)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	v.Parse = buildParse(f, model)
	s.renderAdd(w, r, http.StatusOK, v)
}

// fetchF95 reads the thread. It reports false after writing a response.
func (s *Server) fetchF95(w http.ResponseWriter, r *http.Request, v ui.AddView, f *fetched) bool {
	th, err := s.f95.FetchThread(r.Context(), f.spec.ExternalID)
	notice := func(status int, tone, text string) bool {
		v.Notice = &ui.AddNotice{Tone: tone, Text: text}
		s.renderAdd(w, r, status, v)
		return false
	}
	switch {
	case errors.Is(err, f95.ErrBusy):
		return notice(http.StatusServiceUnavailable, "warn", "F95 busy, try again.")
	case errors.Is(err, f95.ErrCookieInvalid):
		s.alertCookieInvalid(r.Context())
		f.pending = true
		if th == nil { // login page: nothing readable
			return true
		}
	case errors.Is(err, f95.ErrRestricted):
		return notice(http.StatusUnprocessableEntity, "bad", "That thread is restricted or no longer available on F95.")
	case errors.Is(err, f95.ErrBlocked):
		return notice(http.StatusBadGateway, "bad", "F95 is blocking or rate limiting requests. Try again later.")
	case errors.Is(err, f95.ErrParse):
		return notice(http.StatusBadGateway, "bad", "F95 answered with a page that could not be read.")
	case err != nil:
		s.respondError(w, r, err)
		return false
	}
	f.name, f.version, f.changeKey, f.devStatus = th.Name, th.Version, th.Version, string(th.DevStatus)
	f.coverURL, f.gotDetail = th.CoverURL, true
	if th.ThreadUpdated != "" {
		f.threadUpdated = th.ThreadUpdated + "T00:00:00Z"
	}
	if th.LoggedIn {
		f.genreText, f.f95Tags = th.GenreText, th.Tags
	} else {
		f.pending = true
	}
	return true
}

var itchByRe = regexp.MustCompile(`\s+by\s+[^\s].*$`)

// fetchItch reads the page. It reports false after writing a response.
func (s *Server) fetchItch(w http.ResponseWriter, r *http.Request, v ui.AddView, f *fetched) bool {
	page, err := s.itch.FetchPage(r.Context(), f.spec.URL)
	if err != nil {
		text := "The itch.io page could not be read."
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, itch.ErrNotFound):
			text, status = "That itch.io page was not found.", http.StatusUnprocessableEntity
		case errors.Is(err, itch.ErrBlocked):
			text = "itch.io is rate limiting requests. Try again later."
		default:
			s.log.Warn("add: itch page", "err", err)
		}
		v.Notice = &ui.AddNotice{Tone: "bad", Text: text}
		s.renderAdd(w, r, status, v)
		return false
	}
	f.name = strings.TrimSpace(itchByRe.ReplaceAllString(page.Title, ""))
	f.gotDetail = true
	if page.Trackable {
		f.version, f.changeKey = page.Token, page.ChangeKey()
		if !page.Updated.IsZero() {
			f.threadUpdated = clock.Timestamp(page.Updated)
		}
	}
	return true
}

// buildParse turns the fetch result and parse-check model into the panels view.
func buildParse(f fetched, m tags.ParseCheckModel) *ui.AddParse {
	p := &ui.AddParse{
		Hidden: f.hidden(), Name: f.name, Version: f.version, DevStatus: f.devStatus, CoverURL: f.coverURL,
		Pending: f.pending, HasGenre: strings.TrimSpace(f.genreText) != "",
	}
	switch f.spec.Kind {
	case domain.SourceF95Thread:
		p.SourceLabel = "F95 thread #" + f.spec.ExternalID
		p.OpenURL = f.spec.URL
	case domain.SourceItchio:
		p.SourceLabel, p.OpenURL = "itch.io page", f.spec.URL
	default:
		p.SourceLabel, p.OpenURL = "Manual link", f.spec.URL
	}
	row := func(prefix string, i int, e tags.Entry) ui.ParseRow {
		target := e.Target.Label
		if target == "" {
			target = e.Target.Slug
		}
		return ui.ParseRow{
			Prefix: prefix, Index: i, Raw: e.Raw, Target: target, Reason: e.Reason, Qualifier: e.Qualifier,
			Note: e.Note, Accept: e.Accept, Prose: e.Reason == tags.ReasonProse, NewCustom: e.Target.Kind == tags.KindCustom,
		}
	}
	for i, e := range m.NeedsLook {
		p.Look = append(p.Look, row("nl", i, e))
	}
	for i, e := range m.Exact {
		p.Exact = append(p.Exact, row("ex", i, e))
	}
	for i, e := range m.F95Only {
		p.F95Only = append(p.F95Only, ui.F95OnlyRow{Index: i, Slug: e.Slug, Label: e.Label, Accept: e.Accept})
	}
	p.Accepted = countAccepted(m)
	p.Segments = segments(f.genreText, m)
	return p
}

func countAccepted(m tags.ParseCheckModel) int {
	n := 0
	for _, e := range append(append([]tags.Entry{}, m.Exact...), m.NeedsLook...) {
		if e.Accept && (e.Target.Slug != "" || e.Target.Label != "") {
			n++
		}
	}
	for _, e := range m.F95Only {
		if e.Accept {
			n++
		}
	}
	return n
}

// segments splits the Genre text into plain runs and highlighted phrases, in
// text order. A phrase that is not found verbatim is simply not highlighted.
func segments(text string, m tags.ParseCheckModel) []ui.GenreSegment {
	type hit struct {
		at, end int
		class   string
	}
	var hits []hit
	all := make([]tags.Entry, 0, len(m.Exact)+len(m.NeedsLook))
	all = append(all, m.Exact...)
	all = append(all, m.NeedsLook...)
	taken := func(a, b int) bool {
		for _, h := range hits {
			if a < h.end && h.at < b {
				return true
			}
		}
		return false
	}
	// Entries come exact-first; search from the start for each, skipping taken spans.
	for _, e := range all {
		from := 0
		for {
			i := strings.Index(text[from:], e.Raw)
			if i < 0 || e.Raw == "" {
				break
			}
			at := from + i
			if !taken(at, at+len(e.Raw)) {
				hits = append(hits, hit{at, at + len(e.Raw), highlightClass(e)})
				break
			}
			from = at + 1
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	var out []ui.GenreSegment
	pos := 0
	for _, h := range hits {
		if h.at > pos {
			out = append(out, ui.GenreSegment{Text: text[pos:h.at]})
		}
		out = append(out, ui.GenreSegment{Text: text[h.at:h.end], Class: h.class})
		pos = h.end
	}
	if pos < len(text) {
		out = append(out, ui.GenreSegment{Text: text[pos:]})
	}
	return out
}

func highlightClass(e tags.Entry) string {
	switch e.Reason {
	case tags.ReasonProse:
		return "prose"
	case tags.ReasonPossible:
		return "marker"
	case tags.ReasonNoMatch:
		return "custom"
	case tags.ReasonSynonym:
		return "synonym"
	}
	return "exact"
}

// overlay applies the submitted decisions to the freshly parsed model. Rows are
// matched by their raw phrase; a phrase the form does not mention keeps its default.
func (s *Server) overlay(ctx context.Context, form url.Values, m *tags.ParseCheckModel) error {
	known, err := s.tags.Tags(ctx)
	if err != nil {
		return err
	}
	resolve := func(text string, e tags.Entry) tags.TagRef {
		text = strings.TrimSpace(text)
		if text == "" {
			if e.Suggested.Slug != "" || e.Suggested.Label != "" {
				return e.Suggested
			}
			text = e.Raw
		}
		key := genre.Key(text)
		if key == genre.Key(e.Suggested.Label) || key == genre.Key(e.Suggested.Slug) {
			return e.Suggested
		}
		for _, t := range known {
			if genre.Key(t.Slug) == key || genre.Key(t.Label) == key {
				return t
			}
		}
		return tags.TagRef{Kind: tags.KindCustom, Label: text}
	}
	apply := func(prefix string, list []tags.Entry) {
		// A split phrase ("a/b") yields several entries with the same Raw; they
		// map to the posted rows with that Raw in order.
		rows := map[string][]int{}
		for i := 0; form.Has(fmt.Sprintf("%s.%d.raw", prefix, i)); i++ {
			raw := form.Get(fmt.Sprintf("%s.%d.raw", prefix, i))
			rows[raw] = append(rows[raw], i)
		}
		for k := range list {
			idx := rows[list[k].Raw]
			if len(idx) == 0 {
				continue
			}
			i := idx[0]
			rows[list[k].Raw] = idx[1:]
			f := func(n string) string { return form.Get(fmt.Sprintf("%s.%d.%s", prefix, i, n)) }
			list[k].Accept = f("accept") == "1"
			if prefix == "nl" {
				list[k].Target = resolve(f("target"), list[k])
				if q := f("qual"); q != "" {
					list[k].Qualifier = q
				}
			}
		}
	}
	apply("nl", m.NeedsLook)
	apply("ex", m.Exact)
	for i := range m.F95Only {
		for j := 0; form.Has(fmt.Sprintf("fo.%d.slug", j)); j++ {
			if form.Get(fmt.Sprintf("fo.%d.slug", j)) == m.F95Only[i].Slug {
				m.F95Only[i].Accept = form.Get(fmt.Sprintf("fo.%d.accept", j)) == "1"
			}
		}
	}
	return nil
}

func (s *Server) addConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.respondError(w, r, fmt.Errorf("%w: %v", domain.ErrValidation, err))
		return
	}
	form := r.PostForm
	ctx := r.Context()
	kind := domain.SourceKind(form.Get("kind"))
	if !kind.Valid() {
		s.respondError(w, r, fmt.Errorf("%w: source kind", domain.ErrValidation))
		return
	}
	spec := games.SourceSpec{Kind: kind, ExternalID: form.Get("ext"), URL: form.Get("url")}
	pending := form.Get("pending") == "true"
	fetchedPage := form.Get("fetched") == "true"
	name := strings.TrimSpace(form.Get("name"))
	if name == "" && kind == domain.SourceF95Thread && pending {
		name = "F95 thread " + spec.ExternalID
	}
	var f95Tags []f95.Tag
	if raw := form.Get("f95_tags"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &f95Tags); err != nil {
			s.respondError(w, r, fmt.Errorf("%w: f95 tags", domain.ErrValidation))
			return
		}
	}
	model := tags.ParseCheckModel{Exact: []tags.Entry{}, NeedsLook: []tags.Entry{}, F95Only: []tags.F95OnlyEntry{}}
	if !pending {
		var err error
		if model, err = s.tags.ParseCheck(ctx, form.Get("genre_text"), f95Tags); err != nil {
			s.respondError(w, r, err)
			return
		}
		if err := s.overlay(ctx, form, &model); err != nil {
			s.respondError(w, r, err)
			return
		}
	}

	params := games.CreateParams{
		Name: name, PlayStatus: domain.PlayStatus(form.Get("play")), Source: spec,
		LatestVersion: form.Get("version"), ChangeKey: form.Get("change_key"),
		DevStatus: domain.DevStatus(form.Get("dev_status")), ThreadUpdatedAt: form.Get("thread_updated"),
		GenreText: form.Get("genre_text"), DetailsPending: pending,
	}

	var runID int64
	finishRun := func(string) {}
	if fetchedPage && s.refresher != nil {
		var err error
		if runID, finishRun, err = s.refresher.StartManualRun(ctx); err != nil {
			s.respondError(w, r, err)
			return
		}
	}

	var created games.Created
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		if created, err = s.games.CreateTx(ctx, q, params); err != nil {
			return err
		}
		now := clock.Timestamp(s.clock.Now())
		if fetchedPage {
			if _, err = s.games.ApplySourceDetail(ctx, q, created.Source.ID, games.Detail{DetailsPending: pending}); err != nil {
				return err
			}
			step := "detail"
			if kind == domain.SourceItchio {
				step = "itch_page"
			}
			if runID != 0 {
				if _, err = q.InsertCheckResult(ctx, sqlcgen.InsertCheckResultParams{
					RunID: runID, SourceID: sql.NullInt64{Int64: created.Source.ID, Valid: true},
					Step: step, Outcome: "fetched", Attempts: 1, At: now,
				}); err != nil {
					return err
				}
			}
		}
		if err = s.tags.ApplyAddTx(ctx, q, created.Game.ID, model); err != nil {
			return err
		}
		if pending {
			return q.EnqueueDetailFetch(ctx, sqlcgen.EnqueueDetailFetchParams{
				SourceID: created.Source.ID, Reason: "added", Budget: "routine", EnqueuedAt: now,
			})
		}
		return nil
	})
	if err != nil {
		finishRun("failed")
		var conflict *games.ConflictError
		if errors.As(err, &conflict) {
			g, gerr := s.store.Queries().GetGame(ctx, conflict.GameID)
			if gerr != nil {
				s.respondError(w, r, gerr)
				return
			}
			s.renderAdd(w, r, http.StatusConflict, ui.AddView{Kind: string(kind), Tracked: &ui.AddTracked{GameID: g.ID, Name: g.Name}})
			return
		}
		s.respondError(w, r, err)
		return
	}
	finishRun("ok")

	if cover := form.Get("cover_url"); cover != "" {
		if err := s.games.FetchCover(ctx, created.Game.ID, cover); err != nil {
			s.log.Warn("add: cover download failed", "game_id", created.Game.ID, "err", err)
		}
	}
	http.Redirect(w, r, "/games/"+strconv.FormatInt(created.Game.ID, 10), http.StatusSeeOther)
}

// alertCookieInvalid sends the once-per-invalidation push (R-NOTIF-4); a push
// failure is logged, never surfaced to the request.
func (s *Server) alertCookieInvalid(ctx context.Context) {
	if s.notify == nil {
		return
	}
	if err := s.notify.CookieInvalid(ctx); err != nil {
		s.log.Warn("cookie invalid push failed", "err", err)
	}
}
