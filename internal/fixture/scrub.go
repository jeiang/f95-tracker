// Package fixture records scrubbed F95 thread pages for the parser tests
// (R-TEST-2). A page is only returned when no known secret pattern remains.
package fixture

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

const (
	// dropTags are never needed by the parser; dropChrome is site chrome where the
	// viewer's identity lives (nav bar, menus, sidebar, footer).
	dropTags    = "script, style, noscript, link, meta, svg, iframe"
	dropChrome  = ".p-nav, .p-navSticky, .p-sectionLinks, .offCanvasMenu, .p-body-sidebar, .p-footer, .notices, .p-pageWrapper > header"
	minSecretN  = 4
	placeholder = "SCRUBBED"
)

var (
	userIDRe  = regexp.MustCompile(`userId:\s*(\d+)`)
	csrfTok   = regexp.MustCompile(`\b\d{9,11},[0-9a-f]{32}\b`)
	xfTokenRe = regexp.MustCompile(`(name="_xfToken"\s+value=")([^"]*)"`)
	dataCsrf  = regexp.MustCompile(`(data-csrf=")([^"]*)"`)
	maskedTok = regexp.MustCompile(`(/masked/[^/"'\s<>]+/\d+/)([^"'\s<>]+)`)
	badgeRe   = regexp.MustCompile(`(data-badge=")\d+"`)
	cookieRe  = regexp.MustCompile(`\bxf_(?:user|tfa_trust|session|csrf)=[^\s;"'<>]+`)
)

// Scrub minimizes a captured page (drops scripts, styles and replies), removes
// csrf/masked-link tokens and the viewer's identity, then verifies nothing
// secret is left. secrets are extra literal values that must not survive
// (cookie values, the username).
func Scrub(page []byte, secrets ...string) ([]byte, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("fixture: parse page: %w", err)
	}
	doc.Find(dropTags).Remove()
	doc.Find(dropChrome).Remove()
	doc.Find("article.message--post").Each(func(i int, s *goquery.Selection) {
		if i > 0 {
			s.Remove()
		}
	})
	collapseSpace(doc.Nodes[0])
	var buf bytes.Buffer
	if err := html.Render(&buf, doc.Nodes[0]); err != nil {
		return nil, fmt.Errorf("fixture: render page: %w", err)
	}
	out := buf.String()

	secrets = append([]string(nil), secrets...)
	if m := userIDRe.FindStringSubmatch(string(page)); m != nil {
		id := m[1]
		if a := regexp.MustCompile(`alt="([^"]+)"[^>]*class="avatar-u` + id + `-`).FindStringSubmatch(string(page)); a != nil {
			secrets = append(secrets, a[1])
		}
		out = replaceNumber(out, id)
	}
	for _, s := range secrets {
		if len(s) >= minSecretN {
			out = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(s)).ReplaceAllString(out, "user")
		}
	}
	out = csrfTok.ReplaceAllString(out, "0000000000,"+placeholder)
	out = xfTokenRe.ReplaceAllString(out, `${1}`+placeholder+`"`)
	out = dataCsrf.ReplaceAllString(out, `${1}`+placeholder+`"`)
	out = maskedTok.ReplaceAllString(out, "${1}"+placeholder)
	out = badgeRe.ReplaceAllString(out, `${1}0"`)
	out = cookieRe.ReplaceAllString(out, "xf_cookie="+placeholder)

	if err := Verify([]byte(out), secrets...); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Verify reports the first secret pattern still present in page, without
// echoing the matched value.
func Verify(page []byte, secrets ...string) error {
	s := string(page)
	switch {
	case csrfTok.MatchString(s):
		return fmt.Errorf("fixture: csrf token remains")
	case cookieRe.MatchString(s):
		return fmt.Errorf("fixture: xf_ cookie assignment remains")
	case leftover(xfTokenRe, s):
		return fmt.Errorf("fixture: _xfToken value remains")
	case leftover(dataCsrf, s):
		return fmt.Errorf("fixture: data-csrf value remains")
	case leftover(maskedTok, s):
		return fmt.Errorf("fixture: masked-link token remains")
	}
	if m := userIDRe.FindStringSubmatch(s); m != nil && m[1] != "0" {
		return fmt.Errorf("fixture: viewer user id remains")
	}
	low := strings.ToLower(s)
	for _, sec := range secrets {
		if len(sec) >= minSecretN && strings.Contains(low, strings.ToLower(sec)) {
			return fmt.Errorf("fixture: a known secret value remains")
		}
	}
	return nil
}

// leftover reports a match of re (value in group 2) whose value is not the placeholder.
func leftover(re *regexp.Regexp, s string) bool {
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if m[2] != placeholder {
			return true
		}
	}
	return false
}

// replaceNumber replaces every occurrence of the digit string id that is not
// part of a longer number with 0.
func replaceNumber(s, id string) string {
	var sb strings.Builder
	for {
		i := strings.Index(s, id)
		if i < 0 {
			sb.WriteString(s)
			return sb.String()
		}
		j := i + len(id)
		if (i > 0 && isDigit(s[i-1])) || (j < len(s) && isDigit(s[j])) {
			sb.WriteString(s[:j])
		} else {
			sb.WriteString(s[:i])
			sb.WriteByte('0')
		}
		s = s[j:]
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

var spaceRun = regexp.MustCompile(`[ \t\r\n]+`)

// collapseSpace folds whitespace runs in text nodes to one space, which a
// browser renders identically; it keeps fixtures small.
func collapseSpace(n *html.Node) {
	if n.Type == html.TextNode {
		n.Data = spaceRun.ReplaceAllString(n.Data, " ")
	}
	if n.Type == html.ElementNode && n.Data == "pre" {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collapseSpace(c)
	}
}
