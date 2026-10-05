package web

import (
	"context"
	"fmt"
	"slices"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// derivePlay applies R-CSV-4 to a Game's Play log and Dev status at display time.
// ok is false when the rule has no answer (a version was played but the Dev
// status is not known yet).
func derivePlay(logEntries int64, dev string) (status domain.PlayStatus, rule string, ok bool) {
	if logEntries == 0 {
		return domain.PlayPlanned, "No version played → Planned", true
	}
	switch domain.DevStatus(dev) {
	case domain.DevCompleted, domain.DevAbandoned:
		return domain.PlayFinished, fmt.Sprintf("Played, and Dev status %s → Finished", ui.DevStatusLabel(dev)), true
	case domain.DevOngoing, domain.DevOnHold:
		return domain.PlayPlaying, fmt.Sprintf("Played, and Dev status %s → Playing", ui.DevStatusLabel(dev)), true
	}
	return "", "Played, but no Dev status yet; the first detail fetch will re-derive it", false
}

func importRow(r sqlcgen.ListImportReviewGamesRow) ui.ImportRow {
	play := domain.PlayStatus(r.PlayStatus)
	derived, rule, ok := derivePlay(r.PlayLogCount, r.DevStatus.String)
	if ok && derived != play {
		rule = fmt.Sprintf("Changed by you; the rule gives %s", ui.PlayStatusLabel(derived))
	}
	return ui.ImportRow{ID: r.ID, Name: r.Name, Play: play, Rule: rule, Dev: r.DevStatus.String, Version: r.LastPlayedVersion.String}
}

// importReviewRows lists every Game awaiting review, unfiltered.
func (s *Server) importReviewRows(ctx context.Context) ([]ui.ImportRow, error) {
	rows, err := s.store.Queries().ListImportReviewGames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ui.ImportRow, len(rows))
	for i, r := range rows {
		out[i] = importRow(r)
	}
	return out, nil
}

// buildImportReview filters the rows by derived Play status; an unknown filter shows all.
func buildImportReview(all []ui.ImportRow, filter string) ui.ImportReviewView {
	counts := map[domain.PlayStatus]int{}
	for _, r := range all {
		counts[r.Play]++
	}
	v := ui.ImportReviewView{Total: len(all), Counts: []ui.ImportFilterOption{{Label: "all", Count: len(all)}}}
	for _, ps := range ui.PlayStatuses {
		if counts[ps] > 0 {
			v.Counts = append(v.Counts, ui.ImportFilterOption{Value: string(ps), Label: ui.PlayStatusLabel(ps), Count: counts[ps]})
		}
	}
	if counts[domain.PlayStatus(filter)] > 0 {
		v.Filter = filter
	}
	for _, r := range all {
		if v.Filter == "" || string(r.Play) == v.Filter {
			v.Rows = append(v.Rows, r)
		}
	}
	return v
}

func rowIDs(rows []ui.ImportRow) []int64 {
	return slices.Collect(func(yield func(int64) bool) {
		for _, r := range rows {
			if !yield(r.ID) {
				return
			}
		}
	})
}
