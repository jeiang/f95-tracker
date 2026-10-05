package ui

import (
	"strings"

	"github.com/jeiang/f95-tracker/internal/domain"
)

// GameListView is the /games page model. Handlers build it; the page never sees sqlcgen types.
type GameListView struct {
	Rows         []GameRow
	Filters      GameFilters
	Tags         []TagFilterChip
	TagTokens    []string // active tag params ("slug" include, "-slug" exclude), echoed as hidden inputs
	Active       bool     // any filter set (sort alone is not a filter)
	ActiveCount  int
	ImportReview int // Games on the import review list; strip hidden at 0
	TagQueue     int // Games in the tags-to-review queue; strip hidden at 0
}

// GameFilters echoes the validated query parameters back into the filter form.
type GameFilters struct {
	Q       string
	Play    string
	Dev     string
	Rating  string // minimum stars, canonical text ("4.5")
	Behind  bool
	Updates bool
	AllTags bool
	Sort    string
	Dir     string
}

// TagFilterChip is one tri-state tag chip; Href is the link to its next state.
type TagFilterChip struct {
	Slug  string
	Label string
	Count int
	State string // "", "include" or "exclude"
	Href  string
}

// GameRow is one Game in the list.
type GameRow struct {
	ID           int64
	Name         string
	Play         domain.PlayStatus
	Dev          string // display text, "" = none
	DevTone      string
	RatingX2     int
	LastPlayed   VersionLabelProps
	Latest       VersionLabelProps
	Behind       bool
	SourceUpdate string // YYYY-MM-DD the Source reports as updated, "" if unknown
	Badges       []ChipProps
}

// DevStatusLabel is the UI text for a Dev status.
func DevStatusLabel(s string) string {
	switch domain.DevStatus(s) {
	case domain.DevOngoing:
		return "Ongoing"
	case domain.DevCompleted:
		return "Completed"
	case domain.DevAbandoned:
		return "Abandoned"
	case domain.DevOnHold:
		return "On hold"
	}
	return s
}

// DevStatuses lists every Dev status in display order.
var DevStatuses = []domain.DevStatus{domain.DevOngoing, domain.DevCompleted, domain.DevAbandoned, domain.DevOnHold}

func devTone(s string) string {
	switch domain.DevStatus(s) {
	case domain.DevOngoing:
		return "info"
	case domain.DevCompleted:
		return "ok"
	case domain.DevAbandoned:
		return "bad"
	case domain.DevOnHold:
		return "warn"
	}
	return "muted"
}

// DevChip returns the chip for a Dev status ("" = none recorded).
func DevChip(s string) ChipProps {
	if s == "" {
		return ChipProps{Tone: "muted", Text: "no Dev status"}
	}
	return ChipProps{Tone: devTone(s), Text: DevStatusLabel(s)}
}

func playTone(s domain.PlayStatus) string {
	switch s {
	case domain.PlayPlaying:
		return "accent"
	case domain.PlayFinished:
		return "ok"
	case domain.PlayDropped:
		return "bad"
	case domain.PlayOnHold:
		return "warn"
	}
	return "muted"
}

// starsText renders a half-star rating compactly: "★★★★½".
func starsText(x2 int) string {
	return strings.Repeat("★", x2/2) + strings.Repeat("½", x2%2)
}

type sortOption struct{ Value, Label string }

var sortOptions = []sortOption{
	{"", "Update / Behind first"},
	{"name", "Name"},
	{"rating", "Rating"},
	{"play_status", "Play status"},
	{"last_played", "Last played"},
	{"added", "Recently added"},
}

var ratingOptions = []sortOption{
	{"", "Any"}, {"5", "5"}, {"4.5", "4.5 and up"}, {"4", "4 and up"}, {"3.5", "3.5 and up"}, {"3", "3 and up"}, {"2", "2 and up"}, {"1", "1 and up"},
}

var dirOptions = []sortOption{{"", "Natural order"}, {"asc", "Ascending"}, {"desc", "Descending"}}
