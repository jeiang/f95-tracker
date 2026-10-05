package web

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// listParams are the validated /games query parameters. Anything invalid is
// dropped, so a hand-edited URL never errors.
type listParams struct {
	q       string
	play    domain.PlayStatus
	dev     domain.DevStatus
	rating  int // minimum stars x2, 0 = any
	behind  bool
	updates bool
	allTags bool
	sort    games.Sort
	dir     string   // "asc", "desc" or ""
	tags    []string // "slug" includes, "-slug" excludes; known slugs only, one token per slug
}

var validSorts = []games.Sort{games.SortName, games.SortRating, games.SortPlayStatus, games.SortLastPlayed, games.SortAdded}

func parseListParams(v url.Values, known map[string]sqlcgen.ListFilterTagsRow) listParams {
	p := listParams{q: strings.TrimSpace(v.Get("q"))}
	if ps := domain.PlayStatus(v.Get("play")); ps.Valid() {
		p.play = ps
	}
	if ds := domain.DevStatus(v.Get("dev")); ds.Valid() {
		p.dev = ds
	}
	if f, err := strconv.ParseFloat(v.Get("rating"), 64); err == nil {
		if x2 := f * 2; x2 >= 1 && x2 <= 10 && x2 == math.Trunc(x2) {
			p.rating = int(x2)
		}
	}
	p.behind = v.Get("behind") == "1"
	p.updates = v.Get("updates") == "1"
	p.allTags = slices.Contains(v["tags"], "all")
	if s := games.Sort(v.Get("sort")); slices.Contains(validSorts, s) {
		p.sort = s
	}
	if d := v.Get("dir"); d == "asc" || d == "desc" {
		p.dir = d
	}
	seen := map[string]bool{}
	for _, tok := range v["tag"] {
		slug := strings.TrimPrefix(tok, "-")
		if _, ok := known[slug]; ok && !seen[slug] {
			seen[slug] = true
			p.tags = append(p.tags, tok)
		}
	}
	return p
}

func (p listParams) filter(known map[string]sqlcgen.ListFilterTagsRow) games.ListFilter {
	f := games.ListFilter{
		Name: p.q, PlayStatus: p.play, DevStatus: p.dev, MinRatingX2: p.rating,
		Behind: p.behind, Updates: p.updates, AllQualifiers: p.allTags, Sort: p.sort,
	}
	if p.dir != "" {
		desc := p.dir == "desc"
		f.Desc = &desc
	}
	for _, tok := range p.tags {
		if slug, excluded := strings.CutPrefix(tok, "-"); excluded {
			f.ExcludeTags = append(f.ExcludeTags, known[slug].ID)
		} else {
			f.IncludeTags = append(f.IncludeTags, known[tok].ID)
		}
	}
	return f
}

// activeCount is the number of narrowing filters set (sort and direction are not filters).
func (p listParams) activeCount() int {
	n := len(p.tags)
	for _, on := range []bool{p.q != "", p.play != "", p.dev != "", p.rating > 0, p.behind, p.updates} {
		if on {
			n++
		}
	}
	return n
}

// query encodes the parameters with the given tag tokens, for chip links.
func (p listParams) query(tags []string) string {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("q", p.q)
	set("play", string(p.play))
	set("dev", string(p.dev))
	if p.rating > 0 {
		set("rating", strconv.FormatFloat(float64(p.rating)/2, 'f', -1, 64))
	}
	if p.behind {
		v.Set("behind", "1")
	}
	if p.updates {
		v.Set("updates", "1")
	}
	if p.allTags {
		v.Set("tags", "all")
	}
	set("sort", string(p.sort))
	set("dir", p.dir)
	if len(tags) > 0 {
		v["tag"] = tags
	}
	if len(v) == 0 {
		return "/games"
	}
	return "/games?" + v.Encode()
}

// nextTags cycles slug: none -> include -> exclude -> none.
func (p listParams) nextTags(slug string) []string {
	out := make([]string, 0, len(p.tags)+1)
	state := ""
	for _, tok := range p.tags {
		if strings.TrimPrefix(tok, "-") == slug {
			state = map[bool]string{true: "exclude", false: "include"}[strings.HasPrefix(tok, "-")]
			continue
		}
		out = append(out, tok)
	}
	switch state {
	case "":
		out = append(out, slug)
	case "include":
		out = append(out, "-"+slug)
	}
	return out
}

func (s *Server) buildGameList(ctx context.Context, values url.Values) (ui.GameListView, error) {
	q := s.store.Queries()
	tagRows, err := q.ListFilterTags(ctx)
	if err != nil {
		return ui.GameListView{}, err
	}
	known := make(map[string]sqlcgen.ListFilterTagsRow, len(tagRows))
	for _, t := range tagRows {
		if _, dup := known[t.Slug]; !dup {
			known[t.Slug] = t
		}
	}
	p := parseListParams(values, known)

	rows, err := s.games.List(ctx, p.filter(known))
	if err != nil {
		return ui.GameListView{}, err
	}
	v := ui.GameListView{
		Filters: ui.GameFilters{
			Q: p.q, Play: string(p.play), Dev: string(p.dev), Behind: p.behind, Updates: p.updates,
			AllTags: p.allTags, Sort: string(p.sort), Dir: p.dir,
		},
		TagTokens:   p.tags,
		ActiveCount: p.activeCount(),
	}
	v.Active = v.ActiveCount > 0
	if p.rating > 0 {
		v.Filters.Rating = strconv.FormatFloat(float64(p.rating)/2, 'f', -1, 64)
	}
	for _, r := range rows {
		v.Rows = append(v.Rows, gameRow(r))
	}

	// Selected tags first so a chip beyond the visible cut-off stays reachable.
	for pass := 0; pass < 2; pass++ {
		for _, t := range tagRows {
			if known[t.Slug].ID != t.ID {
				continue
			}
			state := tagState(p.tags, t.Slug)
			if (state != "") != (pass == 0) {
				continue
			}
			v.Tags = append(v.Tags, ui.TagFilterChip{
				Slug: t.Slug, Label: t.Label, Count: int(t.Games), State: state, Href: p.query(p.nextTags(t.Slug)),
			})
		}
	}

	n, err := q.CountImportReviewGames(ctx)
	if err != nil {
		return v, err
	}
	v.ImportReview = int(n)
	if n, err = q.CountTagReviewQueue(ctx); err != nil {
		return v, err
	}
	v.TagQueue = int(n)
	return v, nil
}

func tagState(tokens []string, slug string) string {
	for _, tok := range tokens {
		if tok == slug {
			return "include"
		}
		if tok == "-"+slug {
			return "exclude"
		}
	}
	return ""
}

func gameRow(r sqlcgen.ListGamesRow) ui.GameRow {
	g := ui.GameRow{
		ID: r.ID, Name: r.Name, Play: domain.PlayStatus(r.PlayStatus), Dev: r.DevStatus.String,
		RatingX2: int(r.RatingX2.Int64), Behind: r.Behind == 1,
		LastPlayed: ui.VersionLabelProps{Version: r.LastPlayedVersion.String, Date: r.LastPlayedOn.String},
		Latest:     ui.VersionLabelProps{Version: r.LatestVersion.String},
	}
	if len(r.ThreadUpdatedAt.String) >= 10 {
		g.SourceUpdate = r.ThreadUpdatedAt.String[:10]
		g.Latest.Date = g.SourceUpdate
	}
	add := func(tone, text, title string) {
		g.Badges = append(g.Badges, ui.ChipProps{Tone: tone, Text: text, Title: title})
	}
	if r.HasUpdate == 1 {
		add("accent", "Update", "A newer version than your last played")
	}
	if r.Behind == 1 {
		add("warn", "Behind", "Last played version is older than the latest")
	}
	if r.DetailsPending == 1 {
		add("info", "details pending", "Waiting for the Source details to be fetched")
	}
	if r.UnavailableAt.Valid {
		add("bad", "Source unavailable", "The Source page could not be reached")
	}
	if r.ReviewState.String == "pending" || r.ReviewState.String == "skipped" {
		add("accent", "tags to review", "Tag review "+r.ReviewState.String)
	}
	if r.ImportReview == 1 {
		add("accent", "check Play status", "Imported from CSV; Play status was derived")
	}
	return g
}
