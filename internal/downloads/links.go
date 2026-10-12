package downloads

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/jeiang/f95-tracker/internal/domain"
)

// ErrMasked marks an F95 masked link (f95zone.to/masked/...). They resolve only in the user's
// browser, so they are rejected and never stored (INV-25). It is also a domain.ErrValidation.
var ErrMasked = fmt.Errorf("%w: masked URL", domain.ErrValidation)

// Hosts the worker can download from (R-DL-4).
const (
	HostPixeldrain = "pixeldrain"
	HostMega       = "mega"
	HostGofile     = "gofile"
)

// SupportedHosts lists the hosts with a worker adapter, in default order.
var SupportedHosts = []string{HostPixeldrain, HostMega, HostGofile}

// Mirror platforms; the empty string means unknown.
const (
	PlatformLinux    = "linux"
	PlatformWinLinux = "win_linux"
	PlatformWin      = "win"
	PlatformMac      = "mac"
	PlatformAndroid  = "android"
	PlatformOther    = "other"
)

// LinkInput is one submitted link: the client's platform and label are optional.
type LinkInput struct{ URL, Platform, Label string }

// Link is a classified link, ready to store as a mirror.
type Link struct {
	URL, Host, Platform string
	Supported           bool
}

// ClassifyHost returns the mirror host of a URL host name: pixeldrain, mega or gofile for the
// supported hosts, otherwise its registrable domain.
func ClassifyHost(hostname string) (host string, supported bool) {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	reg, err := publicsuffix.EffectiveTLDPlusOne(hostname)
	if err != nil {
		reg = hostname
	}
	switch reg {
	case "pixeldrain.com":
		return HostPixeldrain, true
	case "mega.nz", "mega.io", "mega.co.nz":
		return HostMega, true
	case "gofile.io":
		return HostGofile, true
	}
	return reg, false
}

var (
	winRE   = regexp.MustCompile(`\b(win|windows|pc)\b`)
	linuxRE = regexp.MustCompile(`\blinux\b`)
	macRE   = regexp.MustCompile(`\b(mac|macos|osx)\b`)
	androRE = regexp.MustCompile(`\bandroid\b|\.apk\b`)
)

// GuessPlatform reads a platform off a link label or URL; "" when it says nothing.
func GuessPlatform(text string) string {
	t := strings.ToLower(text)
	switch {
	case androRE.MatchString(t):
		return PlatformAndroid
	case macRE.MatchString(t):
		return PlatformMac
	case linuxRE.MatchString(t) && winRE.MatchString(t):
		return PlatformWinLinux
	case linuxRE.MatchString(t):
		return PlatformLinux
	case winRE.MatchString(t):
		return PlatformWin
	}
	return ""
}

func validPlatform(p string) bool {
	return slices.Contains([]string{"", PlatformLinux, PlatformWinLinux, PlatformWin, PlatformMac, PlatformAndroid, PlatformOther}, p)
}

// Classify validates one submitted link. Masked URLs yield ErrMasked; anything else unusable
// yields a domain.ErrValidation.
func Classify(in LinkInput) (Link, error) {
	raw := strings.TrimSpace(in.URL)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Link{}, fmt.Errorf("%w: %q is not an http(s) URL", domain.ErrValidation, truncate(raw, 80))
	}
	host, supported := ClassifyHost(u.Hostname())
	if host == "f95zone.to" && strings.HasPrefix(u.Path, "/masked/") {
		return Link{}, ErrMasked
	}
	platform := strings.TrimSpace(in.Platform)
	if !validPlatform(platform) {
		return Link{}, fmt.Errorf("%w: unknown platform %q", domain.ErrValidation, platform)
	}
	if platform == "" {
		platform = GuessPlatform(in.Label + " " + u.Path)
	}
	return Link{URL: u.String(), Host: host, Platform: platform, Supported: supported}, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ClassifyAll classifies every input, dropping repeated URLs. Any masked URL fails the whole
// submission (ErrMasked) so nothing from it is stored.
func ClassifyAll(in []LinkInput) ([]Link, error) {
	var out []Link
	seen := map[string]bool{}
	var masked bool
	var firstErr error
	for _, li := range in {
		l, err := Classify(li)
		switch {
		case errors.Is(err, ErrMasked):
			masked = true
		case err != nil:
			firstErr = cmp.Or(firstErr, err)
		case !seen[l.URL]:
			seen[l.URL] = true
			out = append(out, l)
		}
	}
	if masked {
		return nil, ErrMasked
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no links", domain.ErrValidation)
	}
	return out, nil
}

// platformOrders is the platform order of each preference: the preferred platform first, then
// the closest alternatives (default Linux > Win/Linux > Win).
var platformOrders = map[string][]string{
	PlatformLinux:    {PlatformLinux, PlatformWinLinux, PlatformWin},
	PlatformWinLinux: {PlatformWinLinux, PlatformLinux, PlatformWin},
	PlatformWin:      {PlatformWin, PlatformWinLinux, PlatformLinux},
}

// platformRank ranks a platform for a preference; unknown platforms come after the three
// desktop ones, other platforms (mac, android) last.
func platformRank(pref, platform string) int {
	order, ok := platformOrders[pref]
	if !ok {
		order = platformOrders[PlatformLinux]
	}
	switch i := slices.Index(order, platform); {
	case i >= 0:
		return i
	case platform == "":
		return len(order)
	}
	return len(order) + 1
}

// Order sorts links per R-DL-4: supported first, by (platform rank for pref, host rank in
// hostOrder), then unsupported in submitted order. The sort is stable.
func Order(links []Link, pref string, hostOrder []string) []Link {
	out := slices.Clone(links)
	hostRank := func(h string) int {
		if i := slices.Index(hostOrder, h); i >= 0 {
			return i
		}
		return len(hostOrder)
	}
	slices.SortStableFunc(out, func(a, b Link) int {
		switch {
		case a.Supported != b.Supported:
			if a.Supported {
				return -1
			}
			return 1
		case !a.Supported:
			return 0
		}
		if d := platformRank(pref, a.Platform) - platformRank(pref, b.Platform); d != 0 {
			return d
		}
		return hostRank(a.Host) - hostRank(b.Host)
	})
	return out
}
