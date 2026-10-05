package ui

// ReviewView is the /games/{id}/review page model.
type ReviewView struct {
	GameID   int64
	GameName string
	Version  string // the played version being reviewed; empty when unknown
	Present  []ReviewItem
	Reviewed []ReviewItem // present tags unchanged since the last completed review (collapsed)
	Other    []ReviewItem // planned and optional tags
}

// ReviewItem is one checklist row. Verdict is the prefilled choice ("" = unset).
type ReviewItem struct {
	ID              int64
	Label           string
	Custom          bool
	Qualifier       string
	Origin          string // display text
	Note            string
	Verdict         string
	IsNew           bool
	Promoted        bool
	RemovedAtSource bool
}

// MarkedCount is how many Present tags have a prefilled or chosen verdict.
func (v ReviewView) MarkedCount() int {
	n := 0
	for _, t := range v.Present {
		if t.Verdict != "" {
			n++
		}
	}
	return n
}

// QueueView is the /queue page model.
type QueueView struct {
	Rows []QueueItem
}

// QueueItem is one Game in the tags-to-review queue.
type QueueItem struct {
	GameID     int64
	Name       string
	Version    string // last played version; empty when none
	State      string // "pending" or "skipped"
	ToReview   int    // present tags still unverified
	ReviewHref string
}
