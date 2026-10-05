package web

import (
	"context"
	"database/sql"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func TestDerivePlay(t *testing.T) {
	for _, tc := range []struct {
		name string
		logs int64
		dev  string
		want domain.PlayStatus
		ok   bool
	}{
		{"no version", 0, "completed", domain.PlayPlanned, true},
		{"played completed", 2, "completed", domain.PlayFinished, true},
		{"played abandoned", 1, "abandoned", domain.PlayFinished, true},
		{"played ongoing", 1, "ongoing", domain.PlayPlaying, true},
		{"played on hold", 1, "on_hold", domain.PlayPlaying, true},
		{"played, dev unknown", 1, "", "", false},
	} {
		got, rule, ok := derivePlay(tc.logs, tc.dev)
		if got != tc.want || ok != tc.ok || rule == "" {
			t.Errorf("%s: got %q %q %v", tc.name, got, rule, ok)
		}
	}
}

func TestImportReview(t *testing.T) {
	e := newSetEnv(t)
	ctx := context.Background()
	mk := func(name string, ps domain.PlayStatus, dev domain.DevStatus, review bool, version string) int64 {
		g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: name, PlayStatus: ps, DevStatus: dev, ImportReview: review})
		if version != "" {
			testutil.InsertPlayLog(t, e.store, g.ID, version, "")
		}
		return g.ID
	}
	planned := mk("Alpha", domain.PlayPlanned, "", true, "")
	finished := mk("Bravo", domain.PlayFinished, domain.DevCompleted, true, "v1.0")
	playing := mk("Charlie", domain.PlayPlaying, domain.DevOngoing, true, "v0.5")
	changed := mk("Delta", domain.PlayDropped, domain.DevOngoing, true, "v0.2")
	other := mk("Echo", domain.PlayPlaying, domain.DevOngoing, false, "v1")

	page := e.req("GET", "/import-review", nil, false).Body.String()
	for _, want := range []string{"Alpha", "Bravo", "Charlie", "Delta", "No version played", "Dev status Completed → Finished", "the rule gives Playing", "Select all shown", `href="/import-review"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(page, "Echo") {
		t.Error("a Game not on the review list is shown")
	}

	// filter by derived (current) Play status
	filtered := e.req("GET", "/import-review?derived=finished", nil, true).Body.String()
	if !strings.Contains(filtered, "Bravo") || strings.Contains(filtered, "Alpha") || strings.Contains(filtered, "<html") {
		t.Errorf("filter: %s", filtered)
	}
	if all := e.req("GET", "/import-review?derived=bogus", nil, false).Body.String(); !strings.Contains(all, "Alpha") {
		t.Error("unknown filter must show everything")
	}

	status := func(id int64) (string, int64) {
		g, err := e.store.Queries().GetGame(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return g.PlayStatus, g.ImportReview
	}

	// bulk set: changes Play status, keeps rows on the list; Games off the list are untouched
	rec := e.req("POST", "/import-review", url.Values{"action": {"set"}, "play": {"on_hold"}, "id": {i64s(planned), i64s(finished), i64s(other)}}, true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), "Play status changed on 2 Games") {
		t.Fatalf("bulk set: %d %s", rec.Code, rec.Body)
	}
	if ps, rv := status(planned); ps != "on_hold" || rv != 1 {
		t.Errorf("planned -> %s review=%d", ps, rv)
	}
	if ps, _ := status(other); ps != "playing" {
		t.Errorf("Game off the review list was changed: %s", ps)
	}
	if rec := e.req("POST", "/import-review", url.Values{"action": {"set"}, "play": {"bogus"}, "id": {i64s(planned)}}, true); rec.Code != 422 {
		t.Errorf("bad status: %d", rec.Code)
	}
	if rec := e.req("POST", "/import-review", url.Values{"action": {"confirm"}}, true); rec.Code != 422 {
		t.Errorf("empty selection: %d", rec.Code)
	}

	// confirm one row
	rec = e.req("POST", "/import-review", url.Values{"confirm_one": {i64s(planned)}}, true)
	if !strings.Contains(rec.Body.String(), "1 Game confirmed") {
		t.Fatalf("confirm one: %d %s", rec.Code, rec.Body)
	}
	if ps, rv := status(planned); rv != 0 || ps != "on_hold" {
		t.Errorf("confirmed row: %s review=%d (Play status must be kept)", ps, rv)
	}

	// confirm selection
	e.req("POST", "/import-review", url.Values{"action": {"confirm"}, "id": {i64s(finished)}}, true)
	if _, rv := status(finished); rv != 0 {
		t.Error("selection not confirmed")
	}
	if tab := e.req("GET", "/games", nil, false).Body.String(); !strings.Contains(tab, `href="/import-review"`) {
		t.Error("tab must stay while rows remain")
	}

	// confirm all shown under a filter touches only the shown rows
	rec = e.req("POST", "/import-review", url.Values{"action": {"confirm-shown"}, "derived": {"playing"}, "shown": {i64s(playing)}}, true)
	if rec.Code != 200 || rec.Header().Get("HX-Refresh") != "" {
		t.Fatalf("confirm shown: %d %v", rec.Code, rec.Header())
	}
	if _, rv := status(playing); rv != 0 {
		t.Error("shown row not confirmed")
	}
	if _, rv := status(changed); rv != 1 {
		t.Error("row outside the shown list was confirmed")
	}

	// last row: list empties and the tab disappears
	rec = e.req("POST", "/import-review", url.Values{"action": {"confirm-shown"}, "shown": {i64s(changed)}}, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Refresh") != "true" || !strings.Contains(rec.Body.String(), "Every imported Game has a confirmed Play status") {
		t.Fatalf("last confirm: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if tab := e.req("GET", "/games", nil, false).Body.String(); strings.Contains(tab, `href="/import-review"`) {
		t.Error("Import review tab still shown with no rows")
	}
	if page := e.req("GET", "/import-review", nil, false).Body.String(); !strings.Contains(page, "Every imported Game has a confirmed Play status") {
		t.Error("empty state missing")
	}
}

func TestImportReviewNoJSRedirects(t *testing.T) {
	e := newSetEnv(t)
	g, _ := testutil.InsertGame(t, e.store, testutil.GameSpec{Name: "Solo", ImportReview: true})
	rec := e.req("POST", "/import-review", url.Values{"confirm_one": {i64s(g.ID)}, "derived": {"planned"}}, false)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || loc != "/import-review?confirmed=1&derived=planned" {
		t.Fatalf("%d %q", rec.Code, loc)
	}
	if body := e.req("GET", loc, nil, false).Body.String(); !strings.Contains(body, "1 Game confirmed") {
		t.Error("redirected page lacks the result note")
	}
}

func TestImportRuleNeverContradictsStoredStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		play  domain.PlayStatus
		logs  int64
		dev   string
		want  string
		avoid string
	}{
		{"finished from CSV flags before any fetch", domain.PlayFinished, 1, "", "CSV flags", "Playing"},
		{"playing from CSV flags before any fetch", domain.PlayPlaying, 1, "", "CSV flags", "Finished"},
		{"planned though played, no Dev status", domain.PlayPlanned, 1, "", "Changed by you", "provisional"},
		{"live Dev status matches", domain.PlayFinished, 1, "completed", "→ Finished", "Changed"},
		{"live Dev status differs", domain.PlayDropped, 1, "completed", "Changed by you", "→ Finished"},
	} {
		r := importRow(sqlcgen.ListImportReviewGamesRow{PlayStatus: string(tc.play), PlayLogCount: tc.logs,
			DevStatus: sql.NullString{String: tc.dev, Valid: tc.dev != ""}})
		if !strings.Contains(r.Rule, tc.want) || strings.Contains(strings.ToLower(r.Rule), strings.ToLower(tc.avoid)) {
			t.Errorf("%s: rule %q", tc.name, r.Rule)
		}
	}
}
