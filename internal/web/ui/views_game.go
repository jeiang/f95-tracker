package ui

import (
	"github.com/a-h/templ"
	"strconv"

	"github.com/jeiang/f95-tracker/internal/domain"
)

// GameDetailView is the /games/{id} page model.
type GameDetailView struct {
	ID       int64
	Name     string
	Cover    string // cover URL, "" = placeholder
	Initials string

	PrimaryID   int64
	Sources     []SourceRow
	Dev         string // primary Source Dev status, "" = none
	DevEditable bool   // primary Source is not F95
	CanRefresh  bool   // primary Source is F95 or itch.io

	Latest        VersionLabelProps
	LastPlayed    VersionLabelProps
	LastPlayedOn  string // YYYY-MM-DD, "" = undated or not played
	SourceUpdated string // YYYY-MM-DD, "" = unknown
	LastChecked   string // "YYYY-MM-DD HH:MM" UTC, "" = never
	Update        bool
	Behind        bool

	DetailsPending bool
	Unavailable    string // "" or the reason/date line shown with Re-enable checks
	ChecksOff      bool   // checks disabled and not unavailable: "not trackable, update by hand"

	RefreshMsg    string // result of the last Refresh from Source
	RefreshFailed bool

	Play       domain.PlayStatus
	PlayAlerts bool
	RatingX2   int

	Log         []PlayLogRow
	MarkVersion string
	MarkDate    string
	ReviewHref  string // pending / skipped Tag review link, "" = none

	TagGroups  []TagGroup
	TagSummary string
	KnownTags  []string
	GenreText  string // raw Genre text of the primary F95 Source; "" if none
}

// SourceRow is one Source link of the Game.
type SourceRow struct {
	ID      int64
	Label   string
	URL     string
	Primary bool
}

// PlayLogRow is one Play log entry; the newest is first.
type PlayLogRow struct {
	ID       int64
	Version  string
	Date     string // "" = undated (imported only)
	Imported bool
	Last     bool
}

// TagGroup is the Game tags with one qualifier.
type TagGroup struct {
	Qualifier string // present, planned or optional
	Title     string
	Rows      []GameTagRow
}

// GameTagRow is one Game tag.
type GameTagRow struct {
	ID           int64
	Label        string
	Custom       bool
	Qualifier    string
	Verification domain.Verification
	Origin       string
	Note         string
	Phrase       string // Genre phrase that produced it, "" if none
	CanMap       bool   // came from a Genre phrase
	New          bool
	Promoted     bool
	Removed      bool
	F95Only      bool
}

func tagGroupID(q string) string { return "tags-" + q }

func gameURL(id int64, rest string) string {
	return "/games/" + strconv.FormatInt(id, 10) + rest
}

func tagURL(gameID, tagID int64) string {
	return gameURL(gameID, "/tags/"+strconv.FormatInt(tagID, 10))
}

func logURL(gameID, entryID int64, rest string) string {
	return gameURL(gameID, "/play-log/"+strconv.FormatInt(entryID, 10)+rest)
}

func sourceURL(gameID, sid int64) string {
	return gameURL(gameID, "/sources/"+strconv.FormatInt(sid, 10))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func alertHint(alerts bool) string {
	if alerts {
		return "You’ll get an alert when this Game has an Update."
	}
	return "No alerts for Updates in this Play status."
}

func hxDetail(url string) templ.Attributes {
	return templ.Attributes{"hx-post": url, "hx-target": "#game-detail", "hx-swap": "outerHTML"}
}

func hxTags(url string) templ.Attributes {
	return templ.Attributes{"hx-post": url, "hx-target": "#tag-list", "hx-swap": "outerHTML"}
}

func hxChange(url string) templ.Attributes {
	return templ.Attributes{"hx-post": url, "hx-target": "#game-detail", "hx-swap": "outerHTML", "hx-trigger": "submit, change"}
}
