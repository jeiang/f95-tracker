package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/tags"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type reviewEnv struct {
	t     *testing.T
	store *db.Store
	s     *Server
	h     http.Handler
	cook  string
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	store := testutil.NewStore(t)
	fake := testutil.NewOIDCFake(t)
	s := newServer(t, store, fake)
	h, c := signedIn(t, s, fake)
	return &reviewEnv{t: t, store: store, s: s, h: h, cook: c.Name + "=" + c.Value}
}

// game seeds a played Game with tags parsed from genre text and the F95 list slugs.
func (e *reviewEnv) game(genre string, f95Slugs ...string) int64 {
	e.t.Helper()
	ctx := context.Background()
	g, _ := testutil.InsertGame(e.t, e.store, testutil.GameSpec{Name: "Review Game"})
	testutil.InsertPlayLog(e.t, e.store, g.ID, "v1", "2026-02-01")
	var list []f95.Tag
	for _, sl := range f95Slugs {
		list = append(list, f95.Tag{Slug: sl, Label: sl})
	}
	m, err := e.s.tags.ParseCheck(ctx, genre, list)
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if err := e.s.tags.ApplyAddTx(ctx, q, g.ID, m); err != nil {
			return err
		}
		return e.s.tags.EnsurePending(ctx, q, g.ID)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return g.ID
}

func (e *reviewEnv) tagIDs(game int64) map[string]int64 {
	e.t.Helper()
	rows, err := e.s.tags.GameTags(context.Background(), game)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.TagSlug] = r.ID
	}
	return out
}

func (e *reviewEnv) req(method, target string, form url.Values, hx bool) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Header.Set("Cookie", e.cook)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *reviewEnv) queue() []tags.QueueRow {
	e.t.Helper()
	q, err := e.s.tags.Queue(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func checkedVerdict(body string, id int64) string {
	re := regexp.MustCompile(fmt.Sprintf(`name="v-%d" value="([a-z_]+)" checked`, id))
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func TestReviewPagePrefill(t *testing.T) {
	e := newReviewEnv(t)
	g := e.game("Harem, Titfuck, Vore\nPlanned: Pregnancy", "harem")
	ids := e.tagIDs(g)
	if err := e.s.tags.SetVerification(context.Background(), ids["vore"], tags.Wrong); err != nil {
		t.Fatal(err)
	}
	rec := e.req("GET", fmt.Sprintf("/games/%d/review?version=v1", g), nil, false)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for slug, want := range map[string]string{"harem": "correct", "titfuck": "", "vore": "wrong", "pregnancy": ""} {
		if got := checkedVerdict(body, ids[slug]); got != want {
			t.Errorf("%s prefill = %q, want %q", slug, got, want)
		}
	}
	for _, want := range []string{"Save review", "Skip for now", "Planned &amp; optional tags", "Now present"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestReviewSaveCompleteLeavesQueue(t *testing.T) {
	e := newReviewEnv(t)
	g := e.game("Harem, Titfuck\nPlanned: Pregnancy", "harem")
	ids := e.tagIDs(g)
	rec := e.req("POST", fmt.Sprintf("/games/%d/review", g), url.Values{
		fmt.Sprintf("v-%d", ids["harem"]):     {"correct"},
		fmt.Sprintf("v-%d", ids["titfuck"]):   {"wrong"},
		fmt.Sprintf("v-%d", ids["pregnancy"]): {"now_present"},
	}, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/games/%d", g) {
		t.Fatalf("save = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.queue()) != 0 {
		t.Error("completed Game still queued")
	}
	rows, _ := e.s.tags.GameTags(context.Background(), g)
	for _, r := range rows {
		if r.TagSlug == "pregnancy" && (r.Qualifier != tags.QualPresent || r.Verification != tags.Confirmed) {
			t.Errorf("pregnancy = %s/%s", r.Qualifier, r.Verification)
		}
	}
}

func TestReviewPartialSaveKeepsPending(t *testing.T) {
	e := newReviewEnv(t)
	g := e.game("Harem, Titfuck", "harem")
	ids := e.tagIDs(g)
	rec := e.req("POST", fmt.Sprintf("/games/%d/review", g), url.Values{fmt.Sprintf("v-%d", ids["harem"]): {"correct"}, fmt.Sprintf("v-%d", ids["titfuck"]): {""}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	q := e.queue()
	if len(q) != 1 || q[0].State != tags.ReviewPending || q[0].UnverifiedCount != 1 {
		t.Fatalf("queue = %+v", q)
	}
}

func TestReviewSkipListsInQueue(t *testing.T) {
	e := newReviewEnv(t)
	g := e.game("Harem")
	rec := e.req("POST", fmt.Sprintf("/games/%d/review/skip", g), url.Values{}, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/queue" {
		t.Fatalf("skip = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	q := e.queue()
	if len(q) != 1 || q[0].State != tags.ReviewSkipped {
		t.Fatalf("queue = %+v", q)
	}
	body := e.req("GET", "/queue", nil, false).Body.String()
	if !strings.Contains(body, "Review Game") || !strings.Contains(body, "skipped") || !strings.Contains(body, fmt.Sprintf("/games/%d/review?version=v1", g)) {
		t.Errorf("queue page missing skipped Game row")
	}
}

func TestReviewUnknownGame(t *testing.T) {
	e := newReviewEnv(t)
	for _, c := range []struct{ method, target string }{
		{"GET", "/games/999/review"}, {"POST", "/games/999/review"}, {"POST", "/games/999/review/skip"}, {"GET", "/games/abc/review"},
	} {
		var form url.Values
		if c.method == "POST" {
			form = url.Values{}
		}
		if rec := e.req(c.method, c.target, form, false); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d", c.method, c.target, rec.Code)
		}
	}
}

func TestQueueEmptyState(t *testing.T) {
	e := newReviewEnv(t)
	rec := e.req("GET", "/queue", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "No tags waiting for review.") {
		t.Fatalf("empty queue = %d", rec.Code)
	}
}

func TestReviewHTMXBehaviour(t *testing.T) {
	e := newReviewEnv(t)
	g := e.game("Harem, Titfuck", "harem")
	target := fmt.Sprintf("/games/%d/review", g)

	if body := e.req("GET", target, nil, true).Body.String(); strings.Contains(body, "<html") || !strings.Contains(body, `id="tag-list"`) {
		t.Error("htmx GET must return only the tag-list fragment")
	}
	if body := e.req("GET", target, nil, false).Body.String(); !strings.Contains(body, "<html") {
		t.Error("plain GET must return the full page")
	}
	if body := e.req("GET", "/queue", nil, true).Body.String(); strings.Contains(body, "<html") || !strings.Contains(body, `id="queue-list"`) {
		t.Error("htmx queue must return only the list fragment")
	}

	rec := e.req("POST", target, url.Values{"v-99999": {"correct"}}, true)
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusNotFound {
		t.Fatalf("foreign tag via htmx = %d", rec.Code)
	}
	if rec.Header().Get("HX-Retarget") != "#error-slot" {
		t.Error("htmx error must retarget #error-slot")
	}

	rec = e.req("POST", target+"/skip", url.Values{}, true)
	if rec.Header().Get("HX-Redirect") != "/queue" || rec.Header().Get("Location") != "" {
		t.Errorf("htmx skip headers = %v", rec.Header())
	}
	rec = e.req("POST", target, url.Values{}, true)
	if rec.Header().Get("HX-Redirect") != fmt.Sprintf("/games/%d", g) {
		t.Errorf("htmx save headers = %v", rec.Header())
	}
}
