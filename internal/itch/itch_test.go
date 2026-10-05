package itch

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

type harness struct {
	c     *Client
	fake  *testutil.ItchFake
	clk   *clock.Fake
	sleep []time.Duration
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{fake: testutil.NewItchFake(t), clk: clock.NewFake(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))}
	h.c = NewClient(Options{
		Clock:   h.clk,
		Version: "1.2.3",
		BaseURL: h.fake.URL,
		Sleep: func(_ context.Context, d time.Duration) error {
			h.sleep = append(h.sleep, d)
			h.clk.Advance(d)
			return nil
		},
	})
	return h
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var samples = []struct{ name, file, path string }{
	{"s1", "s1.html", "/lycoris-radiata"},
	{"s2", "s2.html", "/my-new-second-chance"},
	{"s3", "s3.html", "/alchemy-shop"},
}

func TestFetchPageGolden(t *testing.T) {
	for _, s := range samples {
		t.Run(s.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.Set(s.path, fixture(t, s.file))
			p, err := h.c.FetchPage(context.Background(), "https://x.itch.io"+s.path)
			if err != nil {
				t.Fatal(err)
			}
			testutil.Golden(t, s.name, struct {
				Page      *Page
				ChangeKey string
			}{p, p.ChangeKey()})
			reqs := h.fake.Requests()
			if len(reqs) != 1 || reqs[0].UserAgent != "f95-tracker/1.2.3" || reqs[0].Cookie != "" {
				t.Errorf("requests = %+v", reqs)
			}
		})
	}
}

func TestTrackable(t *testing.T) {
	h := newHarness(t)
	for _, s := range samples {
		h.fake.Set(s.path, fixture(t, s.file))
	}
	want := map[string]bool{"/lycoris-radiata": true, "/my-new-second-chance": true, "/alchemy-shop": false}
	for path, w := range want {
		p, err := h.c.FetchPage(context.Background(), "https://x.itch.io"+path)
		if err != nil {
			t.Fatal(err)
		}
		if p.Trackable != w {
			t.Errorf("%s Trackable = %v, want %v", path, p.Trackable, w)
		}
	}
}

func TestSpacing(t *testing.T) {
	h := newHarness(t)
	h.fake.Set("/a", fixture(t, "s1.html"))
	for range 3 {
		if _, err := h.c.FetchPage(context.Background(), "https://x.itch.io/a"); err != nil {
			t.Fatal(err)
		}
	}
	// first request is immediate; the next two each wait the full 3s.
	if want := []time.Duration{3 * time.Second, 3 * time.Second}; !slices.Equal(h.sleep, want) {
		t.Errorf("sleeps = %v, want %v", h.sleep, want)
	}
	// time already elapsed counts towards the spacing
	h.sleep = nil
	h.clk.Advance(2 * time.Second)
	if _, err := h.c.FetchPage(context.Background(), "https://x.itch.io/a"); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{time.Second}; !slices.Equal(h.sleep, want) {
		t.Errorf("sleeps = %v, want %v", h.sleep, want)
	}
}

func TestLadder(t *testing.T) {
	ctx := context.Background()
	t.Run("recovers", func(t *testing.T) {
		h := newHarness(t)
		h.fake.Set("/a", fixture(t, "s1.html"))
		h.fake.Respond("/a", 429, 429)
		if _, err := h.c.FetchPage(ctx, "https://x.itch.io/a"); err != nil {
			t.Fatal(err)
		}
		// 1 min, then 3s spacing already satisfied by the wait, 5 min.
		if want := []time.Duration{time.Minute, 5 * time.Minute}; !slices.Equal(h.sleep, want) {
			t.Errorf("sleeps = %v, want %v", h.sleep, want)
		}
		if n := len(h.fake.Requests()); n != 3 {
			t.Errorf("requests = %d, want 3", n)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		h := newHarness(t)
		h.fake.Set("/a", fixture(t, "s1.html"))
		h.fake.Respond("/a", 429, 429, 429, 429)
		_, err := h.c.FetchPage(ctx, "https://x.itch.io/a")
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("err = %v, want ErrBlocked", err)
		}
		if want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}; !slices.Equal(h.sleep, want) {
			t.Errorf("sleeps = %v, want %v", h.sleep, want)
		}
		if n := len(h.fake.Requests()); n != 4 {
			t.Errorf("requests = %d, want 4", n)
		}
	})
}

func TestNotFoundAndErrors(t *testing.T) {
	for _, status := range []int{404, 410} {
		h := newHarness(t)
		h.fake.Respond("/gone", status)
		if _, err := h.c.FetchPage(context.Background(), "https://x.itch.io/gone"); !errors.Is(err, ErrNotFound) {
			t.Errorf("status %d: err = %v, want ErrNotFound", status, err)
		}
	}
	h := newHarness(t)
	h.fake.Respond("/boom", http.StatusInternalServerError)
	_, err := h.c.FetchPage(context.Background(), "https://x.itch.io/boom")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrBlocked) {
		t.Errorf("500: err = %v, want a plain error", err)
	}
}

func TestDevlogTitle(t *testing.T) {
	h := newHarness(t)
	h.fake.Set("/my-new-second-chance/devlog.rss", fixture(t, "s2.devlog.rss"))
	got, err := h.c.DevlogTitle(context.Background(), "https://x.itch.io/my-new-second-chance/")
	if err != nil || got != "The EP33 has just been released!" {
		t.Errorf("got %q, %v", got, err)
	}
	got, err = h.c.DevlogTitle(context.Background(), "https://x.itch.io/alchemy-shop")
	if err != nil || got != "" {
		t.Errorf("404: got %q, %v; want \"\", nil", got, err)
	}
}

func TestChangeKey(t *testing.T) {
	base := Page{
		Title:      "G [v1]",
		Updated:    time.Date(2026, 9, 24, 2, 20, 0, 0, time.UTC),
		Uploads:    []string{"b", "a"},
		VersionMax: 3,
	}
	const want = "updated=2026-09-24T02:20:00Z|title=G [v1]|uploads=a\x1fb|vmax=3"
	if got := base.ChangeKey(); got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
	if base.Uploads[0] != "b" {
		t.Error("ChangeKey reordered the page's uploads")
	}
	swapped := base
	swapped.Uploads = []string{"a", "b"}
	if swapped.ChangeKey() != base.ChangeKey() {
		t.Error("upload order changed the key")
	}
	for name, mut := range map[string]func(*Page){
		"updated": func(p *Page) { p.Updated = p.Updated.Add(time.Minute) },
		"title":   func(p *Page) { p.Title = "G [v2]" },
		"uploads": func(p *Page) { p.Uploads = []string{"a", "c"} },
		"vmax":    func(p *Page) { p.VersionMax = 4 },
	} {
		p := base
		mut(&p)
		if p.ChangeKey() == base.ChangeKey() {
			t.Errorf("%s change did not alter the key", name)
		}
	}
	if got := (&Page{}).ChangeKey(); got != "updated=-|title=|uploads=|vmax=-" {
		t.Errorf("empty key = %q", got)
	}
}

func TestToken(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		uploads []string
		want    string
	}{
		{"last bracket group", "Game [v0.3] [EP33] by Dev", nil, "EP33"},
		{"excluded groups skipped", "My New Second Chance [EP33] [FREE] by X", nil, "EP33"},
		{"excluded case-insensitive", "Game [0.7b] [Demo] [PC] [Android]", nil, "0.7b"},
		{"only excluded falls to uploads", "Game [Free]", []string{"Game v1.2a Win"}, "v1.2a"},
		{"upload version", "Game by Dev", []string{"readme", "Game 0.12.3 PC"}, "0.12.3"},
		{"upload episode", "Game", []string{"(new) MNSC - EP33 - PC"}, "EP33"},
		{"upload chapter", "Game", []string{"Lycoris Radiata Ch7 PC MEGA"}, "Ch7"},
		{"upload chapter dotted", "Game", []string{"Thing Ch. 12"}, "Ch. 12"},
		{"title bracket beats uploads", "Game [Ch2]", []string{"Game v9.9"}, "Ch2"},
		{"none", "Alchemy Shop - NSFW TF Game by jjambong", nil, ""},
		{"no false positive in words", "Game", []string{"Sketch 5", "stepping"}, ""},
		{"empty brackets", "Game []", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := token(tc.title, tc.uploads); got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
		})
	}
}
