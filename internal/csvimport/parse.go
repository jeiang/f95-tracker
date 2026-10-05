// Package csvimport is the one-time, idempotent import of the legacy
// spreadsheet (R-CSV).
package csvimport

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/games"
)

// restricted threads stay unreadable even logged in; they import as manual Games (R-CSV-7).
var restricted = map[string]bool{"94891": true, "151517": true}

// browserOnly itch.io pages have no downloadable build, so checks start disabled (R-ITCH-5, R-CSV-7).
var browserOnly = map[string]bool{"https://jjambong.itch.io/alchemy-shop": true}

// F95Base is the origin of canonical thread URLs written for F95 Games.
const F95Base = "https://f95zone.to"

var threadLink = regexp.MustCompile(`/threads/(?:[^/]*\.)?(\d+)/?`)

// Row is one CSV line.
type Row struct {
	Line      int
	Name      string
	Version   string // "" when the sheet has "-"
	DevStatus domain.DevStatus
	RatingX2  int // 0 = none
	Spec      games.SourceSpec
	// Converted marks a restricted F95 thread imported as a manual Source.
	Converted bool
}

// F95 reports whether the row is an F95 thread Game.
func (r Row) F95() bool { return r.Spec.Kind == domain.SourceF95Thread }

// key identifies the Game a row belongs to (INV-5): thread id, or kind+url otherwise.
func (r Row) key() string {
	if r.F95() {
		return "f95:" + r.Spec.ExternalID
	}
	return string(r.Spec.Kind) + ":" + r.Spec.URL
}

func dash(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" {
		return ""
	}
	return s
}

// Parse reads and classifies every row of the sheet, in file order.
func Parse(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("csv header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	for _, h := range []string{"Name", "Version", "Abandoned", "Completed", "On Hold", "Rating", "URL Code", "Link"} {
		if _, ok := col[h]; !ok {
			return nil, fmt.Errorf("csv header: missing column %q", h)
		}
	}
	var rows []Row
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line, _ := cr.FieldPos(0)
		get := func(h string) string {
			if i := col[h]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if get("Name") == "" && get("Link") == "" && get("URL Code") == "" {
			continue
		}
		row, err := parseRow(get)
		if err != nil {
			return nil, fmt.Errorf("csv line %d: %w", line, err)
		}
		row.Line = line
		rows = append(rows, row)
	}
	return rows, nil
}

func parseRow(get func(string) string) (Row, error) {
	row := Row{Name: get("Name"), Version: dash(get("Version"))}
	if row.Name == "" {
		return row, fmt.Errorf("name is empty")
	}
	switch {
	case strings.EqualFold(get("Abandoned"), "TRUE"):
		row.DevStatus = domain.DevAbandoned
	case strings.EqualFold(get("Completed"), "TRUE"):
		row.DevStatus = domain.DevCompleted
	case strings.EqualFold(get("On Hold"), "TRUE"):
		row.DevStatus = domain.DevOnHold
	default:
		row.DevStatus = domain.DevOngoing
	}
	if v := dash(get("Rating")); v != "" {
		stars, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return row, fmt.Errorf("rating %q", v)
		}
		if row.RatingX2, err = games.RatingX2(stars); err != nil {
			return row, err
		}
	}

	link := get("Link")
	id := dash(get("URL Code"))
	if id == "" {
		if m := threadLink.FindStringSubmatch(link); m != nil && strings.Contains(link, "f95zone") {
			id = m[1]
		}
	}
	if _, err := strconv.ParseUint(id, 10, 64); id != "" && err != nil {
		return row, fmt.Errorf("thread id %q", id)
	}
	if id != "" {
		threadURL := F95Base + "/threads/" + id + "/"
		if restricted[id] {
			row.Spec = games.SourceSpec{Kind: domain.SourceManual, URL: threadURL}
			row.Converted = true
		} else {
			row.Spec = games.SourceSpec{Kind: domain.SourceF95Thread, ExternalID: id, URL: threadURL}
		}
		return row, nil
	}

	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return row, fmt.Errorf("link %q is not a URL", link)
	}
	if strings.HasSuffix(u.Host, ".itch.io") {
		slug := path0(u.Path)
		if slug == "" {
			return row, fmt.Errorf("itch.io link %q has no game slug", link)
		}
		row.Spec = games.SourceSpec{Kind: domain.SourceItchio, ExternalID: slug, URL: link}
		return row, nil
	}
	row.Spec = games.SourceSpec{Kind: domain.SourceManual, URL: link}
	return row, nil
}

// path0 is the last non-empty path segment (the itch.io game slug).
func path0(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	return parts[len(parts)-1]
}

// derivePlayStatus is R-CSV-4: no version → planned; completed/abandoned → finished;
// otherwise (ongoing, on hold) → playing.
func derivePlayStatus(played bool, dev domain.DevStatus) domain.PlayStatus {
	switch {
	case !played:
		return domain.PlayPlanned
	case dev == domain.DevCompleted || dev == domain.DevAbandoned:
		return domain.PlayFinished
	}
	return domain.PlayPlaying
}
