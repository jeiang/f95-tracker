package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// detailNote carries the outcome of Refresh from Source into the page.
type detailNote struct {
	msg    string
	failed bool
}

func sourceLabel(src sqlcgen.Source) string {
	switch domain.SourceKind(src.Kind) {
	case domain.SourceF95Thread:
		return "F95 thread #" + src.ExternalID.String
	case domain.SourceItchio:
		return "itch.io page"
	}
	return "Source link"
}

func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		out = append(out, []rune(w)[0])
		if len(out) == 2 {
			break
		}
	}
	return strings.ToUpper(string(out))
}

func datePart(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func minutePart(s string) string {
	if len(s) >= 16 {
		return s[:10] + " " + s[11:16]
	}
	return s
}

var originText = map[string]string{
	tags.OriginBoth: "Genre + F95", tags.OriginGenre: "Genre", tags.OriginF95List: "F95", tags.OriginManual: "by hand",
}

func (s *Server) buildGameDetail(ctx context.Context, id int64, note detailNote) (ui.GameDetailView, error) {
	d, err := s.games.Detail(ctx, id)
	if err != nil {
		return ui.GameDetailView{}, err
	}
	p := d.Primary
	v := ui.GameDetailView{
		ID: id, Name: d.Game.Name, Initials: initials(d.Game.Name), PrimaryID: p.ID,
		Dev: p.DevStatus.String, DevEditable: p.Kind != string(domain.SourceF95Thread),
		CanRefresh:    p.Kind != string(domain.SourceManual),
		Latest:        ui.VersionLabelProps{Version: p.LatestVersion.String, Date: datePart(p.ThreadUpdatedAt.String)},
		SourceUpdated: datePart(p.ThreadUpdatedAt.String), LastChecked: minutePart(p.LastCheckedAt.String),
		Update: d.HasUpdate, Behind: d.Behind, DetailsPending: p.DetailsPending == 1,
		GenreText: p.GenreText.String, RefreshMsg: note.msg, RefreshFailed: note.failed,
		Play: domain.PlayStatus(d.Game.PlayStatus), PlayAlerts: d.Alerting, RatingX2: int(d.Game.RatingX2.Int64),
		MarkVersion: p.LatestVersion.String, MarkDate: clock.Date(s.clock.Now()),
	}
	if d.Game.CoverPath.Valid {
		v.Cover = fmt.Sprintf("/covers/%d?v=%s", id, url.QueryEscape(d.Game.CoverFetchedAt.String))
	}
	for _, src := range d.Sources {
		v.Sources = append(v.Sources, ui.SourceRow{ID: src.ID, Label: sourceLabel(src), URL: src.Url, Primary: src.IsPrimary == 1})
	}
	switch {
	case p.UnavailableAt.Valid:
		v.Unavailable = "Unavailable since " + datePart(p.UnavailableAt.String) + "."
		if p.UnavailableReason.Valid {
			v.Unavailable = "Unavailable since " + datePart(p.UnavailableAt.String) + " (" + p.UnavailableReason.String + ")."
		}
	case p.ChecksEnabled == 0:
		v.ChecksOff = true
	}

	lastID := int64(0)
	if lp := d.LastPlayed; lp != nil {
		lastID = lp.PlayLogID
		v.LastPlayed = ui.VersionLabelProps{Version: lp.Version}
		v.LastPlayedOn = lp.PlayedOn.String
		if v.MarkVersion == "" {
			v.MarkVersion = lp.Version
		}
	}
	for _, e := range d.PlayLog {
		v.Log = append(v.Log, ui.PlayLogRow{
			ID: e.ID, Version: e.Version, Date: e.PlayedOn.String,
			Imported: e.Origin == string(domain.PlayLogImported), Last: e.ID == lastID,
		})
	}
	sort.SliceStable(v.Log, func(i, j int) bool {
		a, b := v.Log[i], v.Log[j]
		if (a.Date == "") != (b.Date == "") {
			return b.Date == ""
		}
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return a.ID > b.ID
	})

	switch tr, err := s.store.Queries().GetTagReview(ctx, id); {
	case err == nil && (tr.State == tags.ReviewPending || tr.State == tags.ReviewSkipped):
		v.ReviewHref = fmt.Sprintf("/games/%d/review?version=%s", id, url.QueryEscape(v.LastPlayed.Version))
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return v, err
	}

	rows, err := s.tags.GameTags(ctx, id)
	if err != nil {
		return v, err
	}
	known, err := s.tags.Tags(ctx)
	if err != nil {
		return v, err
	}
	for _, t := range known {
		v.KnownTags = append(v.KnownTags, t.Label)
	}
	byQual := map[string][]ui.GameTagRow{}
	var present, confirmed, wrong int
	for _, r := range rows {
		row := ui.GameTagRow{
			ID: r.ID, Label: r.TagLabel, Custom: r.TagKind == tags.KindCustom, Qualifier: r.Qualifier,
			Verification: domain.Verification(r.Verification), Origin: originText[r.Origin], Note: r.ModifierNote.String,
			Phrase: r.SourcePhrase.String, CanMap: r.SourcePhrase.Valid && (r.Origin == tags.OriginGenre || r.Origin == tags.OriginBoth),
			New: r.IsNew == 1, Promoted: r.Promoted == 1, Removed: r.RemovedAtSourceAt.Valid, F95Only: r.F95Only == 1,
		}
		if r.F95Only == 1 && r.Origin != tags.OriginManual {
			row.Origin = "F95 only"
		}
		byQual[r.Qualifier] = append(byQual[r.Qualifier], row)
		if r.Qualifier == tags.QualPresent {
			present++
			switch r.Verification {
			case tags.Confirmed:
				confirmed++
			case tags.Wrong:
				wrong++
			}
		}
	}
	for _, g := range []struct{ q, title string }{{tags.QualPresent, "Present"}, {tags.QualPlanned, "Planned"}, {tags.QualOptional, "Optional"}} {
		rs := byQual[g.q]
		if len(rs) == 0 {
			continue
		}
		sort.SliceStable(rs, func(i, j int) bool { return strings.ToLower(rs[i].Label) < strings.ToLower(rs[j].Label) })
		v.TagGroups = append(v.TagGroups, ui.TagGroup{Qualifier: g.q, Title: g.title, Rows: rs})
	}
	if present > 0 {
		v.TagSummary = strconv.Itoa(present) + " present · " + strconv.Itoa(confirmed) + " confirmed · " + strconv.Itoa(wrong) + " wrong"
	}
	return v, nil
}
