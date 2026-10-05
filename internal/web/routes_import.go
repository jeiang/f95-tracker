package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/web/ui"
)

// importRoutes: the CSV-import review list (R-CSV-10).
func (s *Server) importRoutes(mux *http.ServeMux) {
	mux.Handle("GET /import-review", s.requireSession(http.HandlerFunc(s.importReviewPage)))
	mux.Handle("POST /import-review", s.requireSession(http.HandlerFunc(s.importReviewAct)))
}

func (s *Server) renderImportReview(w http.ResponseWriter, r *http.Request, v ui.ImportReviewView) {
	s.render(w, r, ui.Layout(s.pageMeta(r, ui.TabImportReview, "Import review"), ui.ImportReviewPage(v)), "import-list")
}

func (s *Server) importReviewPage(w http.ResponseWriter, r *http.Request) {
	all, err := s.importReviewRows(r.Context())
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	s.renderImportReview(w, r, buildImportReview(all, r.URL.Query().Get("derived")))
}

// importReviewAct handles the list form. action is "set" (bulk Play status on the
// selected rows), "confirm" (selected), "confirm-shown" (every row of the
// filtered list, the shown ids), or the per-row Confirm button (confirm_one).
// Only Games still awaiting review can be touched.
func (s *Server) importReviewAct(w http.ResponseWriter, r *http.Request) {
	if err := s.parseForm(w, r); err != nil {
		s.respondError(w, r, err)
		return
	}
	ctx := r.Context()
	all, err := s.importReviewRows(ctx)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	pending := make(map[int64]bool, len(all))
	for _, row := range all {
		pending[row.ID] = true
	}
	ids := func(vals []string) []int64 {
		var out []int64
		for _, v := range vals {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && pending[id] && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out
	}

	action, filter := r.PostFormValue("action"), r.PostFormValue("derived")
	var target []int64
	switch {
	case r.PostFormValue("confirm_one") != "":
		action, target = "confirm", ids([]string{r.PostFormValue("confirm_one")})
	case action == "confirm-shown":
		action, target = "confirm", ids(r.PostForm["shown"])
	case action == "set" || action == "confirm":
		target = ids(r.PostForm["id"])
	default:
		s.respondError(w, r, fmt.Errorf("%w: unknown action", domain.ErrValidation))
		return
	}
	if len(target) == 0 {
		s.respondError(w, r, fmt.Errorf("%w: select at least one Game", domain.ErrValidation))
		return
	}

	var confirmed, set int
	if action == "set" {
		ps := domain.PlayStatus(r.PostFormValue("play"))
		for _, id := range target {
			if err := s.games.SetPlayStatus(ctx, id, ps); err != nil {
				s.respondError(w, r, err)
				return
			}
		}
		set = len(target)
	} else {
		if err := s.games.ClearImportReview(ctx, target...); err != nil {
			s.respondError(w, r, err)
			return
		}
		confirmed = len(target)
	}

	all, err = s.importReviewRows(ctx)
	if err != nil {
		s.respondError(w, r, err)
		return
	}
	v := buildImportReview(all, filter)
	v.Confirmed, v.SetCount = confirmed, set
	if v.Total == 0 && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true") // the Import review tab goes away with the last row
	}
	s.renderImportReview(w, r, v)
}
