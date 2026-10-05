// Package itch checks itch.io game pages (R-ITCH-1..7): one polite HTML GET per
// Game, parsed into a Page whose ChangeKey fingerprints everything that counts
// as an Update.
package itch

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
)

var (
	// ErrNotFound is a 404/410: the caller counts a miss (R-ITCH-6).
	ErrNotFound = errors.New("itch: page not found")
	// ErrBlocked means every retry of the 429 ladder was rate limited; the
	// caller stops itch.io requests for the run (R-ITCH-1).
	ErrBlocked = errors.New("itch: rate limited")
)

const maxBody = 8 << 20

// Options configures a Client. Zero values take the production defaults.
type Options struct {
	HTTP       *http.Client
	Clock      clock.Clock
	Version    string          // app version for the User-Agent; default "dev"
	MinSpacing time.Duration   // default 3s between requests
	Ladder     []time.Duration // waits before each retry after a 429; default 1/5/15 min
	// Sleep waits d or returns ctx's error. Tests pass a func that advances a
	// fake clock; the default blocks on a real timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// BaseURL, when set, replaces the scheme and host of every requested URL
	// (tests point it at the fake).
	BaseURL string
}

// Client fetches itch.io pages sequentially, honouring spacing and the 429 ladder.
type Client struct {
	opt  Options
	slot chan struct{} // serialises requests
	last time.Time
}

func NewClient(o Options) *Client {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.MinSpacing == 0 {
		o.MinSpacing = 3 * time.Second
	}
	if o.Ladder == nil {
		o.Ladder = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	c := &Client{opt: o, slot: make(chan struct{}, 1)}
	c.slot <- struct{}{}
	return c
}

// get returns the body of a 200 response, retrying 429s on the ladder.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	if c.opt.BaseURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("itch: bad url: %w", err)
		}
		b, err := url.Parse(c.opt.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("itch: bad base url: %w", err)
		}
		u.Scheme, u.Host = b.Scheme, b.Host
		rawURL = u.String()
	}
	select {
	case <-c.slot:
		defer func() { c.slot <- struct{}{} }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for attempt := 0; ; attempt++ {
		if !c.last.IsZero() {
			if wait := c.opt.MinSpacing - c.opt.Clock.Now().Sub(c.last); wait > 0 {
				if err := c.opt.Sleep(ctx, wait); err != nil {
					return nil, err
				}
			}
		}
		c.last = c.opt.Clock.Now()
		status, body, err := c.do(ctx, rawURL)
		if err != nil {
			return nil, err
		}
		switch {
		case status == http.StatusOK:
			return body, nil
		case status == http.StatusNotFound || status == http.StatusGone:
			return nil, ErrNotFound
		case status == http.StatusTooManyRequests:
			if attempt >= len(c.opt.Ladder) {
				return nil, ErrBlocked
			}
			if err := c.opt.Sleep(ctx, c.opt.Ladder[attempt]); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("itch: GET %s: HTTP %d", rawURL, status)
		}
	}
}

func (c *Client) do(ctx context.Context, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "f95-tracker/"+c.opt.Version)
	resp, err := c.opt.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("itch: GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("itch: read %s: %w", rawURL, err)
	}
	return resp.StatusCode, body, nil
}

// Page is what one game page tells us.
type Page struct {
	Title      string
	Updated    time.Time // UTC; zero when the page has no Updated row
	Uploads    []string  // names in page order
	VersionMax int       // highest "Version N"; 0 = none
	Token      string    // R-ITCH-4 version token; "" = none
	Trackable  bool      // R-ITCH-5: false for browser-only games
}

// ChangeKey is the R-ITCH-3 fingerprint; upload order does not matter.
func (p *Page) ChangeKey() string {
	updated := "-"
	if !p.Updated.IsZero() {
		updated = clock.Timestamp(p.Updated)
	}
	ups := slices.Clone(p.Uploads)
	slices.Sort(ups)
	vmax := "-"
	if p.VersionMax > 0 {
		vmax = strconv.Itoa(p.VersionMax)
	}
	return "updated=" + updated + "|title=" + p.Title + "|uploads=" + strings.Join(ups, "\x1f") + "|vmax=" + vmax
}

// FetchPage GETs and parses a game page.
func (c *Client) FetchPage(ctx context.Context, gameURL string) (*Page, error) {
	body, err := c.get(ctx, gameURL)
	if err != nil {
		return nil, err
	}
	return parsePage(string(body)), nil
}

// DevlogTitle returns the newest devlog post title; a 404 means no devlog ("", nil).
func (c *Client) DevlogTitle(ctx context.Context, gameURL string) (string, error) {
	body, err := c.get(ctx, strings.TrimRight(gameURL, "/")+"/devlog.rss")
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var feed struct {
		Items []struct {
			Title string `xml:"title"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return "", fmt.Errorf("itch: parse devlog: %w", err)
	}
	if len(feed.Items) == 0 {
		return "", nil
	}
	return strings.TrimSpace(feed.Items[0].Title), nil
}

var (
	titleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	updatedRe = regexp.MustCompile(`<td>Updated</td>\s*<td><abbr[^>]*\btitle="([^"]*)"`)
	uploadRe  = regexp.MustCompile(`class="upload_name"><strong[^>]*>([^<]*)</strong>`)
	versionRe = regexp.MustCompile(`class="version_name">\s*Version (\d+)`)
)

const updatedLayout = "2 January 2006 @ 15:04 UTC"

func parsePage(doc string) *Page {
	p := &Page{}
	if m := titleRe.FindStringSubmatch(doc); m != nil {
		p.Title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	}
	if m := updatedRe.FindStringSubmatch(doc); m != nil {
		if t, err := time.Parse(updatedLayout, html.UnescapeString(m[1])); err == nil {
			p.Updated = t.UTC()
		}
	}
	for _, m := range uploadRe.FindAllStringSubmatch(doc, -1) {
		p.Uploads = append(p.Uploads, strings.TrimSpace(html.UnescapeString(m[1])))
	}
	for _, m := range versionRe.FindAllStringSubmatch(doc, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			p.VersionMax = max(p.VersionMax, n)
		}
	}
	p.Trackable = !p.Updated.IsZero() || len(p.Uploads) > 0
	p.Token = token(p.Title, p.Uploads)
	return p
}

var (
	bracketRe = regexp.MustCompile(`\[([^\[\]]*)\]`)
	uploadTok = regexp.MustCompile(`(?i)\bv?\d+(?:\.\d+)+[a-z]?\b|\bep\d+|\bch\.?\s?\d+`)
	excluded  = []string{"free", "demo", "pc", "win", "mac", "linux", "android"}
)

// token is the R-ITCH-4 heuristic.
func token(title string, uploads []string) string {
	groups := bracketRe.FindAllStringSubmatch(title, -1)
	for i := len(groups) - 1; i >= 0; i-- {
		g := strings.TrimSpace(groups[i][1])
		if g != "" && !slices.Contains(excluded, strings.ToLower(g)) {
			return g
		}
	}
	for _, u := range uploads {
		if m := uploadTok.FindString(u); m != "" {
			return m
		}
	}
	return ""
}
