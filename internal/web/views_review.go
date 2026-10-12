package web

import (
	"context"

	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

func reviewOriginText(t tags.ReviewTag) string {
	switch {
	case t.Origin == tags.OriginBoth:
		return "In the Genre text and the F95 list"
	case t.Origin == tags.OriginGenre:
		return "Genre text only"
	case t.Origin == tags.OriginManual:
		return "Added by hand"
	case t.F95Only:
		return "F95 list only"
	}
	return "F95 list"
}

func reviewItem(t tags.ReviewTag, phrases map[int64]string, posted map[int64]string) ui.ReviewItem {
	it := reviewItemBase(t)
	if p, ok := phrases[t.GameTagID]; ok {
		it.CanSynonym, it.Phrase = true, p
	}
	if v, ok := posted[t.GameTagID]; ok {
		it.Verdict = v
	}
	return it
}

func reviewItemBase(t tags.ReviewTag) ui.ReviewItem {
	return ui.ReviewItem{
		ID: t.GameTagID, Label: t.Tag.Label, Custom: t.Tag.Kind == tags.KindCustom, Qualifier: t.Qualifier,
		Origin: reviewOriginText(t), Note: t.Note, Verdict: t.Verdict,
		IsNew: t.IsNew, Promoted: t.Promoted, RemovedAtSource: t.RemovedAtSource,
	}
}

// buildReview maps the tags service model to the page. After a completed review
// only tags that changed are asked about; the rest sit in a collapsed list.
// posted verdicts (unsaved choices from the form) win over the prefill.
func (s *Server) buildReview(ctx context.Context, gameID int64, version string, posted map[int64]string, notice string) (ui.ReviewView, error) {
	m, err := s.tags.ReviewModel(ctx, gameID)
	if err != nil {
		return ui.ReviewView{}, err
	}
	g, err := s.store.Queries().GetGame(ctx, gameID)
	if err != nil {
		return ui.ReviewView{}, err
	}
	rows, err := s.tags.GameTags(ctx, gameID)
	if err != nil {
		return ui.ReviewView{}, err
	}
	phrases := map[int64]string{}
	for _, r := range rows {
		if tags.CanRemap(r.Origin, r.SourcePhrase) {
			phrases[r.ID] = r.SourcePhrase.String
		}
	}
	known, err := s.tags.Tags(ctx)
	if err != nil {
		return ui.ReviewView{}, err
	}
	queue, err := s.tags.Queue(ctx)
	if err != nil {
		return ui.ReviewView{}, err
	}
	v := ui.ReviewView{GameID: gameID, GameName: g.Name, Version: version, Notice: notice, QueueTotal: len(queue)}
	for i, q := range queue {
		if q.GameID == gameID {
			v.QueuePos = i + 1
		}
	}
	for _, t := range known {
		v.KnownTags = append(v.KnownTags, t.Label)
	}
	for _, t := range m.Present {
		if m.Later && !t.Changed {
			v.Reviewed = append(v.Reviewed, reviewItem(t, phrases, posted))
		} else {
			v.Present = append(v.Present, reviewItem(t, phrases, posted))
		}
	}
	for _, t := range m.Other {
		v.Other = append(v.Other, reviewItem(t, phrases, posted))
	}
	return v, nil
}

func (s *Server) buildQueue(ctx context.Context) (ui.QueueView, error) {
	rows, err := s.tags.Queue(ctx)
	if err != nil {
		return ui.QueueView{}, err
	}
	var v ui.QueueView
	for _, r := range rows {
		it := ui.QueueItem{GameID: r.GameID, Name: r.Name, Version: r.LastPlayedVersion.String, State: r.State, ToReview: int(r.UnverifiedCount)}
		it.ReviewHref = reviewHref(r.GameID, it.Version)
		v.Rows = append(v.Rows, it)
	}
	return v, nil
}
