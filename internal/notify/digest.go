package notify

import (
	"database/sql"
	"fmt"
	"strings"
)

const (
	maxItemLines = 20
	maxBodyBytes = 4000
)

// UpdateItem is one Update row. New marks rows first reported by this digest.
type UpdateItem struct {
	GameID, CheckResultID  int64
	Name                   string
	OldVersion, NewVersion string
	// OldDevStatus/NewDevStatus are set only when the Dev status changed.
	OldDevStatus, NewDevStatus string
	New                        bool
}

// JobLine is a download job line (awaiting links, needs human, or finished).
type JobLine struct {
	JobID, GameID int64
	Text          string
	New           bool
}

// FailedItem is one Check failed entry.
type FailedItem struct {
	GameID, CheckResultID int64
	Source                string // e.g. the Game name or "F95 checker"
	Error                 string
	New                   bool
}

// NeedsYou lists what waits on the user. CookieInvalid and TagsToReview are
// appended context and never make a digest worthy of sending on their own.
type NeedsYou struct {
	CookieInvalid bool
	Jobs          []JobLine
	TagsToReview  int
}

// Digest is the input of SendDigest; RunID 0 means no check run.
type Digest struct {
	RunID             int64
	Updates           []UpdateItem
	NeedsYou          NeedsYou
	FinishedDownloads []JobLine
	CheckFailed       []FailedItem
}

func (d Digest) hasNews() bool {
	for _, u := range d.Updates {
		if u.New {
			return true
		}
	}
	for _, j := range d.NeedsYou.Jobs {
		if j.New {
			return true
		}
	}
	for _, j := range d.FinishedDownloads {
		if j.New {
			return true
		}
	}
	for _, f := range d.CheckFailed {
		if f.New {
			return true
		}
	}
	return false
}

type entry struct {
	text    string
	heading bool
	item    *coverage
}

// coverage is what a rendered item line writes to notification_item.
type coverage struct{ game, result, job sql.NullInt64 }

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

func (u UpdateItem) line() string {
	s := fmt.Sprintf("- %s: %s → %s", u.Name, u.OldVersion, u.NewVersion)
	if u.OldDevStatus != "" && u.OldDevStatus != u.NewDevStatus { // a first-known Dev status is not a change
		s += fmt.Sprintf(" (Dev status: %s → %s)", u.OldDevStatus, u.NewDevStatus)
	}
	return s
}

// entries flattens the digest in section order, omitting empty sections.
func (d Digest) entries() []entry {
	var out []entry
	section := func(heading string, items []entry) {
		if len(items) > 0 {
			out = append(out, entry{text: heading, heading: true})
			out = append(out, items...)
		}
	}
	var items []entry
	for _, u := range d.Updates {
		items = append(items, entry{text: u.line(), item: &coverage{game: nullID(u.GameID), result: nullID(u.CheckResultID)}})
	}
	section("Updates", items)

	items = nil
	if d.NeedsYou.CookieInvalid {
		items = append(items, entry{text: "- F95 cookie is invalid"})
	}
	for _, j := range d.NeedsYou.Jobs {
		items = append(items, entry{text: "- " + j.Text, item: &coverage{game: nullID(j.GameID), job: nullID(j.JobID)}})
	}
	if n := d.NeedsYou.TagsToReview; n > 0 {
		items = append(items, entry{text: fmt.Sprintf("- Tags to review: %d", n)})
	}
	section("Needs you", items)

	items = nil
	for _, j := range d.FinishedDownloads {
		items = append(items, entry{text: "- " + j.Text, item: &coverage{game: nullID(j.GameID), job: nullID(j.JobID)}})
	}
	section("Finished downloads", items)

	items = nil
	for _, f := range d.CheckFailed {
		items = append(items, entry{text: fmt.Sprintf("- %s: %s", f.Source, f.Error), item: &coverage{game: nullID(f.GameID), result: nullID(f.CheckResultID)}})
	}
	section("Check failed", items)
	return out
}

// buildBody renders the digest: 20 item lines at most plus "and N more", then a
// hard cut to 4,000 bytes at a line boundary. It returns the item coverage of
// the lines that made it into the body.
func buildBody(d Digest) (string, []coverage) {
	all := d.entries()
	var kept []entry
	items, more := 0, 0
	for _, e := range all {
		if e.heading {
			kept = append(kept, e)
			continue
		}
		if items == maxItemLines {
			more++
			continue
		}
		items++
		kept = append(kept, e)
	}
	// Headings of sections whose items were all cut have no item after them.
	kept = dropTrailingHeadings(kept)
	for len(kept) > 0 && len(render(kept, more)) > maxBodyBytes {
		if !kept[len(kept)-1].heading {
			more++
		}
		kept = dropTrailingHeadings(kept[:len(kept)-1])
	}
	var cov []coverage
	for _, e := range kept {
		if e.item != nil {
			cov = append(cov, *e.item)
		}
	}
	return render(kept, more), cov
}

func dropTrailingHeadings(es []entry) []entry {
	for len(es) > 0 && es[len(es)-1].heading {
		es = es[:len(es)-1]
	}
	return es
}

func render(es []entry, more int) string {
	var b strings.Builder
	for i, e := range es {
		if e.heading && i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.text)
		b.WriteByte('\n')
	}
	if more > 0 {
		fmt.Fprintf(&b, "\nand %d more\n", more)
	}
	return strings.TrimRight(b.String(), "\n")
}
