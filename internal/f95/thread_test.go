package f95

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

const fixtureDir = "../../testdata/f95"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseThreadGolden(t *testing.T) {
	for _, c := range []struct{ id, file, golden string }{
		{"67494", "67494.html", "thread_67494"},             // ongoing, Genre spoiler
		{"114650", "114650.html", "thread_114650"},          // Completed
		{"92250", "92250.html", "thread_92250"},             // Abandoned
		{"67426", "67426.html", "thread_67426"},             // Onhold
		{"186046", "186046.html", "thread_186046"},          // planned + optional Genre markers
		{"59416", "59416.guest.html", "thread_59416_guest"}, // logged out
	} {
		t.Run(c.golden, func(t *testing.T) {
			body := readFixture(t, c.file)
			th, err := ParseThread(body, c.id, "https://f95zone.to/threads/x."+c.id+"/", loggedInRe.Match(body))
			if err != nil {
				t.Fatal(err)
			}
			testutil.Golden(t, c.golden, th)
		})
	}
}

func TestParseThreadInvariants(t *testing.T) {
	for file, want := range map[string]domain.DevStatus{
		"114650.html": domain.DevCompleted, "92250.html": domain.DevAbandoned,
		"67426.html": domain.DevOnHold, "67494.html": domain.DevOngoing,
	} {
		body := readFixture(t, file)
		th, err := ParseThread(body, "1", "u", true)
		if err != nil {
			t.Fatal(file, err)
		}
		if th.DevStatus != want {
			t.Errorf("%s: dev status %q, want %q", file, th.DevStatus, want)
		}
		if !th.HasGenre || th.GenreText == "" || len(th.Tags) == 0 || th.Version == "" || th.Name == "" {
			t.Errorf("%s: incomplete parse %+v", file, th)
		}
	}
	guest, err := ParseThread(readFixture(t, "59416.guest.html"), "59416", "u", false)
	if err != nil {
		t.Fatal(err)
	}
	if guest.HasGenre || guest.GenreText != "" || guest.LoggedIn {
		t.Errorf("guest page must not claim Genre text: %+v", guest)
	}
}

func TestParseThreadSanity(t *testing.T) {
	good := string(readFixture(t, "67494.html"))
	for name, mutate := range map[string]func(string) string{
		"no title":                func(s string) string { return replaceAll(s, `p-title-value`, `p-title-gone`) },
		"no tag block":            func(s string) string { return replaceAll(s, `js-tagList`, `js-nothing`) },
		"no genre when logged in": func(s string) string { return replaceAll(s, `<b>Genre`, `<b>Gen`) },
		"no version":              func(s string) string { return replaceAll(s, `[v4.34.1 Public] [Story Anon]`, `Story`) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseThread([]byte(mutate(good)), "67494", "u", true)
			if !errors.Is(err, ErrParse) {
				t.Fatalf("err = %v, want ErrParse", err)
			}
		})
	}
}

func TestSplitTitle(t *testing.T) {
	for _, c := range []struct {
		title, name, version, dev string
		ok                        bool
	}{
		{"Out of Touch! [v4.34.1 Public] [Story Anon]", "Out of Touch!", "v4.34.1 Public", "Story Anon", true},
		{"Game [Remake] [Ch.4 v1.0] [Dev]", "Game [Remake]", "Ch.4 v1.0", "Dev", true},
		{"Game [Final]", "Game", "Final", "", true},
		{"No brackets", "", "", "", false},
		{"[v1] [dev]", "", "", "", false},
	} {
		th := &Thread{Title: c.title}
		if ok := splitTitle(th); ok != c.ok || (ok && (th.Name != c.name || th.Version != c.version || th.Developer != c.dev)) {
			t.Errorf("%q -> %+v ok=%v", c.title, th, ok)
		}
	}
}

func replaceAll(s, old, repl string) string {
	if !strings.Contains(s, old) {
		panic("fixture does not contain " + old)
	}
	return strings.ReplaceAll(s, old, repl)
}
