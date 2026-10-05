// Package ui holds the templ components and pages. Pages take plain view-model
// structs (never sqlcgen types) built by handlers in internal/web.
//
// Convention: shared view types live here in views.go; a per-area view model
// (game list rows, tag review, settings, ...) lives in views_<area>.go next to
// that area's <area>.templ files, and is created by the item that owns the area.
// Partials are templ.Fragment("name") blocks inside the page component, addressed
// by stable element ids (#game-table, #tag-list, #play-log, #cookie-status,
// #parse-panels, #error-slot).
package ui

import "github.com/jeiang/f95-tracker/internal/domain"

// Tab identifies the primary navigation entry a page belongs to.
type Tab string

const (
	TabNone         Tab = ""
	TabGames        Tab = "games"
	TabQueue        Tab = "queue"
	TabAdd          Tab = "add"
	TabImportReview Tab = "import-review"
	TabSettings     Tab = "settings"
)

// Banner is a global banner shown on every page (cookie invalid, trust expiring, last check failed).
type Banner struct {
	Tone     string // "warn", "bad" or "info"
	Text     string
	LinkText string
	LinkHref string
}

// PageMeta is what Layout needs from every page.
type PageMeta struct {
	Title            string
	ActiveTab        Tab
	Banners          []Banner
	ShowImportReview bool // only while import review rows exist
}

type navItem struct {
	Tab   Tab
	Label string
	Href  string
}

func (p PageMeta) navItems() []navItem {
	items := []navItem{
		{TabGames, "Games", "/games"},
		{TabQueue, "Queue", "/queue"},
		{TabAdd, "Add", "/games/new"},
	}
	if p.ShowImportReview {
		items = append(items, navItem{TabImportReview, "Import review", "/import-review"})
	}
	return append(items, navItem{TabSettings, "Settings", "/settings"})
}

// PlayStatusLabel is the UI text for a Play status.
func PlayStatusLabel(s domain.PlayStatus) string {
	switch s {
	case domain.PlayPlanned:
		return "Planned"
	case domain.PlayPlaying:
		return "Playing"
	case domain.PlayFinished:
		return "Finished"
	case domain.PlayDropped:
		return "Dropped"
	case domain.PlayOnHold:
		return "On hold"
	}
	return string(s)
}

// PlayStatuses lists every Play status in display order.
var PlayStatuses = []domain.PlayStatus{
	domain.PlayPlanned, domain.PlayPlaying, domain.PlayFinished, domain.PlayDropped, domain.PlayOnHold,
}
