package web

import (
	"context"
	"fmt"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// derivePlay applies R-CSV-4 to a Game's Play log and Dev status at display time.
// ok is false when the rule has no answer: a version was played but the Dev
// status is not fetched yet, so the stored Play status is the import's
// provisional one from the CSV flags (R-CSV-4).
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
	return "", "Provisional, from the CSV flags; the first detail fetch re-derives it from the live Dev status", false
}

func importRow(r sqlcgen.ListImportReviewGamesRow) ui.ImportRow {
	play := domain.PlayStatus(r.PlayStatus)
	derived, rule, ok := derivePlay(r.PlayLogCount, r.DevStatus.String)
	switch {
	case ok && derived != play:
		rule = fmt.Sprintf("Changed by you; the rule gives %s", ui.PlayStatusLabel(derived))
	case !ok && play == domain.PlayPlanned:
		// the import never derives Planned for a played Game
		rule = "Changed by you; the import derived a non-Planned status from the CSV flags"
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
