package downloads

import (
	"errors"
	"slices"
	"testing"

	"github.com/jeiang/f95-tracker/internal/domain"
)

func TestClassifyHost(t *testing.T) {
	cases := []struct {
		url       string
		host      string
		supported bool
	}{
		{"https://pixeldrain.com/u/abc", "pixeldrain", true},
		{"https://www.pixeldrain.com/u/abc", "pixeldrain", true},
		{"https://mega.nz/file/x#k", "mega", true},
		{"https://mega.io/file/x#k", "mega", true},
		{"https://MEGA.NZ/folder/x", "mega", true},
		{"https://gofile.io/d/abc", "gofile", true},
		{"https://store1.gofile.io/d/abc", "gofile", true},
		{"https://workupload.com/file/x", "workupload.com", false},
		{"https://dl.mediafire.com/a/b", "mediafire.com", false},
		{"https://notmega.nz.evil.example/x", "evil.example", false},
	}
	for _, c := range cases {
		l, err := Classify(LinkInput{URL: c.url})
		if err != nil || l.Host != c.host || l.Supported != c.supported {
			t.Errorf("%s: %+v, %v; want %s supported=%v", c.url, l, err, c.host, c.supported)
		}
	}
}

func TestClassifyRejectsMaskedAndGarbage(t *testing.T) {
	for _, u := range []string{
		"https://f95zone.to/masked/abc123/",
		"https://f95zone.to/masked/mega/xyz",
		"  https://F95ZONE.to/masked/1  ",
	} {
		if _, err := Classify(LinkInput{URL: u}); !errors.Is(err, ErrMasked) || !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%q: %v, want ErrMasked", u, err)
		}
	}
	for _, u := range []string{"", "not a url", "ftp://pixeldrain.com/u/a", "javascript:alert(1)", "https://"} {
		if _, err := Classify(LinkInput{URL: u}); !errors.Is(err, domain.ErrValidation) || errors.Is(err, ErrMasked) {
			t.Errorf("%q: %v, want plain validation error", u, err)
		}
	}
	if _, err := Classify(LinkInput{URL: "https://mega.nz/file/x", Platform: "beos"}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("unknown platform: %v", err)
	}
	// A non-masked f95zone link is kept as a manual link.
	if l, err := Classify(LinkInput{URL: "https://f95zone.to/threads/1/"}); err != nil || l.Supported {
		t.Errorf("thread link: %+v %v", l, err)
	}
}

func TestMaskedAnywhereRejectsTheWholeSubmission(t *testing.T) {
	_, err := ClassifyAll([]LinkInput{{URL: "https://pixeldrain.com/u/a"}, {URL: "https://f95zone.to/masked/q"}})
	if !errors.Is(err, ErrMasked) {
		t.Fatalf("got %v", err)
	}
	if _, err := ClassifyAll(nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("empty: %v", err)
	}
	got, err := ClassifyAll([]LinkInput{{URL: "https://pixeldrain.com/u/a"}, {URL: "https://pixeldrain.com/u/a"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("duplicates: %v %v", got, err)
	}
}

func TestGuessPlatform(t *testing.T) {
	cases := map[string]string{
		"Win/Linux":           PlatformWinLinux,
		"Windows, Linux":      PlatformWinLinux,
		"PC":                  PlatformWin,
		"Win":                 PlatformWin,
		"Linux":               PlatformLinux,
		"Mac":                 PlatformMac,
		"macOS":               PlatformMac,
		"Android":             PlatformAndroid,
		"game-v1.apk":         PlatformAndroid,
		"Winter Special":      "",
		"Machine":             "",
		"DOWNLOAD":            "",
		"":                    "",
		"Win (Linux version)": PlatformWinLinux,
	}
	for in, want := range cases {
		if got := GuessPlatform(in); got != want {
			t.Errorf("GuessPlatform(%q) = %q, want %q", in, got, want)
		}
	}
	// The label is used when the client gives no platform; an explicit one wins.
	l, _ := Classify(LinkInput{URL: "https://mega.nz/file/x", Label: "Win/Linux"})
	if l.Platform != PlatformWinLinux {
		t.Errorf("guessed %q", l.Platform)
	}
	l, _ = Classify(LinkInput{URL: "https://mega.nz/file/x", Label: "Win/Linux", Platform: "win"})
	if l.Platform != PlatformWin {
		t.Errorf("explicit %q", l.Platform)
	}
}

func TestOrderPlatformAndHostPreference(t *testing.T) {
	mk := func(host, platform string, supported bool) Link {
		return Link{URL: "https://" + host + "/" + platform, Host: host, Platform: platform, Supported: supported}
	}
	in := []Link{
		mk("workupload.com", "linux", false),
		mk("gofile", "linux", true),
		mk("mega", "win", true),
		mk("pixeldrain", "win", true),
		mk("mega", "linux", true),
		mk("pixeldrain", "win_linux", true),
		mk("mega", "", true),
		mk("mega", "mac", true),
	}
	names := func(ls []Link) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Host+"/"+l.Platform)
		}
		return out
	}
	host := []string{"pixeldrain", "mega", "gofile"}
	cases := []struct {
		pref string
		want []string
	}{
		{"linux", []string{"mega/linux", "gofile/linux", "pixeldrain/win_linux", "pixeldrain/win", "mega/win", "mega/", "mega/mac", "workupload.com/linux"}},
		{"win_linux", []string{"pixeldrain/win_linux", "mega/linux", "gofile/linux", "pixeldrain/win", "mega/win", "mega/", "mega/mac", "workupload.com/linux"}},
		{"win", []string{"pixeldrain/win", "mega/win", "pixeldrain/win_linux", "mega/linux", "gofile/linux", "mega/", "mega/mac", "workupload.com/linux"}},
	}
	for _, c := range cases {
		if got := names(Order(in, c.pref, host)); !slices.Equal(got, c.want) {
			t.Errorf("pref %s:\n got %v\nwant %v", c.pref, got, c.want)
		}
	}
	// A custom host order changes ties within a platform, and unlisted hosts rank last.
	got := names(Order([]Link{mk("pixeldrain", "linux", true), mk("gofile", "linux", true), mk("mega", "linux", true)}, "linux", []string{"gofile", "mega"}))
	if want := []string{"gofile/linux", "mega/linux", "pixeldrain/linux"}; !slices.Equal(got, want) {
		t.Errorf("host order: %v", got)
	}
	if in[0].Host != "workupload.com" {
		t.Error("Order mutated its input")
	}
}
