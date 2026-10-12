package check

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func (d *dailyEnv) jobsOf(gameID int64) []string {
	d.t.Helper()
	rows, err := d.store.Queries().ListOpenDownloadJobsForGame(ctx, gameID)
	if err != nil {
		d.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.State+"/"+r.Trigger+"/"+r.TargetVersion)
	}
	return out
}

func TestUpdateCreatesAwaitingLinksJobOnlyForAlertSetGames(t *testing.T) {
	d := newDaily(t, false)
	a := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v4.0", oldDay)
	b := d.f95Game("Bravo", domain.PlayFinished, "67426", "v1", oldDay)
	c := d.f95Game("Charlie", domain.PlayOnHold, "114650", "v9", oldDay)
	d.fake.SetVersion("67494", "v4.34")
	d.fake.SetVersion("67426", "v2")
	d.fake.SetVersion("114650", "v9") // unchanged

	d.run(Options{})

	if got := d.jobsOf(a.GameID); len(got) != 1 || got[0] != "awaiting_links/update/v4.34" {
		t.Errorf("alert-set Update jobs = %v", got)
	}
	if got := d.jobsOf(b.GameID); len(got) != 0 {
		t.Errorf("non-alert Update created %v", got)
	}
	if got := d.jobsOf(c.GameID); len(got) != 0 {
		t.Errorf("no Update, yet jobs %v", got)
	}

	// The same Update is not detected twice, and a repeat run keeps exactly one open job.
	d.nextDay()
	d.run(Options{})
	if got := d.jobsOf(a.GameID); len(got) != 1 {
		t.Errorf("repeat run jobs = %v", got)
	}
}

func TestUpdateWithOpenJobForSameVersionReusesIt(t *testing.T) {
	d := newDaily(t, false)
	s := d.f95Game("Alpha", domain.PlayPlaying, "67494", "v4.0", oldDay)
	svc := downloads.New(d.store, d.clk, nil)
	pre, _, err := svc.Create(ctx, s.GameID) // button job for the current v4.0
	if err != nil {
		t.Fatal(err)
	}
	d.fake.SetVersion("67494", "v5.0")
	d.run(Options{})
	got := d.jobsOf(s.GameID)
	if len(got) != 2 {
		t.Fatalf("jobs = %v", got)
	}
	if j, _ := svc.Get(ctx, pre.ID); j.State != downloads.AwaitingLinks {
		t.Errorf("the existing job changed: %+v", j)
	}
}

func TestItchUpdateCreatesJob(t *testing.T) {
	d := newDaily(t, false)
	page, _ := os.ReadFile("../itch/testdata/s1.html")
	devlog, _ := os.ReadFile("../itch/testdata/s2.devlog.rss")
	d.ifake.Set("/lycoris-radiata", page)
	d.ifake.Set("/lycoris-radiata/devlog.rss", devlog)
	g, _ := testutil.InsertGame(t, d.store, testutil.GameSpec{Name: "Lycoris", PlayStatus: domain.PlayPlaying, Kind: domain.SourceItchio, URL: "https://kuro.itch.io/lycoris-radiata", ExternalID: "lycoris", ChangeKey: "updated=old", LatestVersion: "old"})
	d.run(Options{})
	if got := d.jobsOf(g.ID); len(got) != 1 || got[0] != "awaiting_links/update/Ch7" {
		t.Errorf("itch jobs = %v", got)
	}
}

func TestDigestDownloadSections(t *testing.T) {
	d := newDaily(t, false)
	base := "http://tracker"
	svc := downloads.New(d.store, d.clk, nil)
	waiting := d.f95Game("Waiting", domain.PlayPlaying, "100", "v1", oldDay)
	stuck := d.f95Game("Stuck", domain.PlayPlaying, "101", "v1", oldDay)
	fin := d.f95Game("Finished", domain.PlayPlaying, "102", "v1", oldDay)
	d.fake.SetVersion("100", "v1")
	d.fake.SetVersion("101", "v1")
	d.fake.SetVersion("102", "v1")

	wj, _, _ := svc.Create(ctx, waiting.GameID)
	sj, _, _ := svc.Create(ctx, stuck.GameID)
	fj, _, _ := svc.Create(ctx, fin.GameID)
	if _, err := svc.IngestLinks(ctx, sj.ID, []downloads.LinkInput{{URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IngestLinks(ctx, fj.ID, []downloads.LinkInput{{URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MarkDone(ctx, fj.ID); err != nil {
		t.Fatal(err)
	}

	// Run 1: awaiting-links and needs-human jobs are Needs you items; the finished one is listed too.
	d.run(Options{})
	body := d.lastBody()
	for _, want := range []string{
		"Needs you",
		"- Waiting v1: awaiting links, " + base + "/downloads/" + strconv.FormatInt(wj.ID, 10) + "/links",
		"- Stuck v1: download needs you (no supported mirror), " + base + "/downloads",
		"Finished downloads",
		"- Finished v1: downloaded",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("digest lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "Needs you") > strings.Index(body, "Finished downloads") {
		t.Errorf("section order:\n%s", body)
	}
	if n := len(d.ntfy.Requests()); n != 1 {
		t.Fatalf("pushes = %v", d.titles())
	}

	// Run 2: nothing is new. The waiting jobs are not worth a digest by themselves, and the
	// finished job is not reported twice.
	d.nextDay()
	d.run(Options{})
	if n := len(d.ntfy.Requests()); n != 1 {
		t.Fatalf("an unchanged backlog was pushed again: %v", d.titles())
	}

	// A job newly needing a human is news again, and then still listed under Needs you.
	d.nextDay()
	if _, err := svc.IngestLinks(ctx, wj.ID, []downloads.LinkInput{{URL: "https://workupload.com/f"}}); err != nil {
		t.Fatal(err)
	}
	d.run(Options{})
	if n := len(d.ntfy.Requests()); n != 2 {
		t.Fatalf("new needs_human job not reported: %v", d.titles())
	}
	body = d.lastBody()
	if !strings.Contains(body, "- Waiting v1: download needs you (no supported mirror)") || strings.Contains(body, "Finished downloads") {
		t.Errorf("digest 2:\n%s", body)
	}
}
