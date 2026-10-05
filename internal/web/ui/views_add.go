package ui

import (
	"encoding/json"
	"strconv"
)

// AddView is the /games/new page model (also the #parse-panels fragment).
type AddView struct {
	Kind    string // "f95_thread", "itchio" or "manual"
	Input   string // link or thread id as typed
	Name    string // manual Game name as typed
	Version string // manual latest version as typed
	TagText string // pasted tag text (itch.io and manual)
	Error   string // invalid-input message under the link field

	Notice  *AddNotice
	Tracked *AddTracked
	Parse   *AddParse
	Vocab   []string // tag labels for the datalist
}

// AddNotice is a message in the panels area (busy, blocked, unavailable).
type AddNotice struct{ Tone, Text string }

// AddTracked is the already-tracked state.
type AddTracked struct {
	GameID int64
	Name   string
}

// AddParse is the fetched Source with the three parse-check panels. The Hidden
// pairs carry the Source across the Confirm POST (no server-side session state).
type AddParse struct {
	Hidden      [][2]string
	SourceLabel string
	Name        string
	Version     string
	DevStatus   string // "" = none
	CoverURL    string
	OpenURL     string
	Pending     bool // cookie invalid or guest page: added with details pending
	Segments    []GenreSegment
	HasGenre    bool
	Look        []ParseRow
	Exact       []ParseRow
	F95Only     []F95OnlyRow
	Accepted    int
}

// GenreSegment is a run of the Genre text; Class is "" for plain text, else
// exact, synonym, custom, marker or prose.
type GenreSegment struct{ Text, Class string }

// ParseRow is one editable Genre phrase. Field names are nl.<i>.* / ex.<i>.*.
type ParseRow struct {
	Prefix    string // "nl" or "ex"
	Index     int
	Raw       string
	Target    string
	Reason    string
	Qualifier string
	Note      string
	Accept    bool
	Prose     bool
	NewCustom bool
}

type F95OnlyRow struct {
	Index  int
	Slug   string
	Label  string
	Accept bool
}

func (r ParseRow) Field(name string) string {
	return r.Prefix + "." + strconv.Itoa(r.Index) + "." + name
}
func (r F95OnlyRow) Field(name string) string { return "fo." + strconv.Itoa(r.Index) + "." + name }

// ReasonText is why a phrase sits under Needs a look.
func (r ParseRow) ReasonText() string {
	switch r.Reason {
	case "synonym":
		return "Matched through a Synonym"
	case "no_match":
		return "No F95 tag or Synonym; becomes a Custom tag"
	case "prose":
		return "Looks like a sentence, not a tag"
	case "possible":
		return "Listed as possible; added as planned"
	}
	return ""
}

// addCopy is the per-source-type wording of the link field.
type addCopy struct {
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Hint        string `json:"hint"`
}

var addCopies = map[string]addCopy{
	"f95_thread": {"F95zone thread link or id", "https://f95zone.to/threads/… or 238127", "We read the title, latest version, Dev status, F95 tags and the Genre text. Nothing is added until you confirm."},
	"itchio":     {"itch.io page link", "https://dev.itch.io/game", "We read the name and the page's change fingerprint. Paste tags below to run them through the parser. Nothing is added until you confirm."},
	"manual":     {"Link", "https://…", "Any page you want to keep a link to. Manual Sources are never checked."},
}

func (v AddView) copy() addCopy {
	if c, ok := addCopies[v.Kind]; ok {
		return c
	}
	return addCopies["f95_thread"]
}

func addCopiesJSON() string {
	b, _ := json.Marshal(addCopies)
	return string(b)
}
