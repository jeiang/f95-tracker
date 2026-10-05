package ui

import "github.com/jeiang/f95-tracker/internal/domain"

// ImportReviewView is the /import-review page model.
type ImportReviewView struct {
	Rows      []ImportRow
	Total     int // all rows awaiting review, before the filter
	Filter    string
	Counts    []ImportFilterOption // "all" first, then each derived Play status that occurs
	Confirmed int                  // Games confirmed by the request that rendered this, 0 = none
	SetCount  int                  // Games whose Play status the request changed, 0 = none
}

// ImportFilterOption is one derived-status filter.
type ImportFilterOption struct {
	Value string // "" = all
	Label string
	Count int
}

// ImportRow is one Game awaiting review.
type ImportRow struct {
	ID      int64
	Name    string
	Play    domain.PlayStatus
	Rule    string // why Play is what it is, computed from the Play log and Dev status
	Dev     string // Dev status ("" = none); CSV flags are recorded as Dev status
	Version string // last played version, "" = none
}
