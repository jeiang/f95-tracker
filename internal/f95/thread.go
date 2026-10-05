package f95

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"github.com/jeiang/f95-tracker/internal/domain"
)

// Tag is one entry of the thread's F95 tag list.
type Tag struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

// Thread is the parsed first post and title of a thread page (R-F95-8).
type Thread struct {
	ID            string           `json:"id"`
	URL           string           `json:"url"`
	Title         string           `json:"title"`
	Name          string           `json:"name"`
	Version       string           `json:"version"`
	Developer     string           `json:"developer"`
	DevStatus     domain.DevStatus `json:"dev_status"`
	Prefixes      []string         `json:"prefixes"`
	Tags          []Tag            `json:"tags"`
	GenreText     string           `json:"genre_text"`
	HasGenre      bool             `json:"has_genre"`
	ThreadUpdated string           `json:"thread_updated"` // YYYY-MM-DD, "" when the OP has none
	CoverURL      string           `json:"cover_url"`
	LoggedIn      bool             `json:"logged_in"`
}

var (
	bracketTail   = regexp.MustCompile(`\s*\[([^\[\]]*)\]\s*$`)
	threadUpdated = regexp.MustCompile(`Thread Updated\s*:\s*(\d{4}-\d{2}-\d{2})`)
	spaceRun      = regexp.MustCompile(`[ \t\r\n\f\xa0]+`)
	blankLines    = regexp.MustCompile(`\n{3,}`)
	genreLabelMax = 60
)

// ParseThread extracts a Thread from a thread page body. loggedIn is the
// page's data-logged-in flag; a logged-in page must carry the Genre block.
// Missing title, version or tag block is ErrParse (R-F95-13).
func ParseThread(body []byte, threadID, pageURL string, loggedIn bool) (*Thread, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParse, err)
	}
	th := &Thread{ID: threadID, URL: pageURL, LoggedIn: loggedIn, DevStatus: domain.DevOngoing, Prefixes: []string{}, Tags: []Tag{}}

	h1 := doc.Find("h1.p-title-value").First()
	if h1.Length() == 0 {
		return nil, fmt.Errorf("%w: thread %s has no title", ErrParse, threadID)
	}
	h1.Find("a.labelLink").Each(func(_ int, a *goquery.Selection) {
		p := clean(a.Text())
		th.Prefixes = append(th.Prefixes, p)
		switch strings.ToLower(p) {
		case "completed":
			th.DevStatus = domain.DevCompleted
		case "onhold":
			th.DevStatus = domain.DevOnHold
		case "abandoned":
			th.DevStatus = domain.DevAbandoned
		}
	})
	h1.Find("a.labelLink, .label-append").Remove()
	th.Title = clean(h1.Text())
	if !splitTitle(th) {
		return nil, fmt.Errorf("%w: thread %s title has no version bracket", ErrParse, threadID)
	}

	tagList := doc.Find(".js-tagList").First()
	if tagList.Length() == 0 {
		return nil, fmt.Errorf("%w: thread %s has no tag block", ErrParse, threadID)
	}
	tagList.Find(`a.tagItem[href^="/tags/"]`).Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		slug := strings.Trim(strings.TrimPrefix(href, "/tags/"), "/")
		th.Tags = append(th.Tags, Tag{Slug: slug, Label: clean(a.Text())})
	})

	op := doc.Find("article.message--post .bbWrapper").First()
	if m := threadUpdated.FindStringSubmatch(blockText(op)); m != nil {
		th.ThreadUpdated = m[1]
	}
	th.CoverURL = coverURL(op)

	genre, found := genreText(op)
	if loggedIn && !found {
		return nil, fmt.Errorf("%w: logged-in thread %s has no Genre block", ErrParse, threadID)
	}
	if loggedIn {
		th.GenreText, th.HasGenre = genre, genre != ""
	}
	return th, nil
}

// splitTitle fills Name, Version and Developer from "Name [version] [developer]".
func splitTitle(th *Thread) bool {
	rest := th.Title
	var groups []string
	for len(groups) < 2 {
		loc := bracketTail.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		groups = append([]string{strings.TrimSpace(rest[loc[2]:loc[3]])}, groups...)
		rest = rest[:loc[0]]
	}
	switch len(groups) {
	case 0:
		return false
	case 1:
		th.Version = groups[0]
	default:
		th.Version, th.Developer = groups[0], groups[1]
	}
	th.Name = strings.TrimSpace(rest)
	return th.Name != "" && th.Version != ""
}

// coverURL is the first OP image on the attachments./preview. host at full size.
func coverURL(op *goquery.Selection) string {
	var out string
	op.Find("img.bbImage").EachWithBreak(func(_ int, img *goquery.Selection) bool {
		src, _ := img.Attr("src")
		if src == "" || strings.HasPrefix(src, "data:") {
			src, _ = img.Attr("data-src")
		}
		u, err := url.Parse(src)
		if err != nil || (!strings.HasPrefix(u.Host, "attachments.") && !strings.HasPrefix(u.Host, "preview.")) {
			return true
		}
		u.Path = strings.Replace(u.Path, "/thumb/", "/", 1)
		out = u.String()
		return false
	})
	return out
}

// genreText finds `<b>Genre…</b>` among the OP's direct children and returns the
// text of the next sibling .bbCodeSpoiler. found is false when there is no such
// label+spoiler pair.
func genreText(op *goquery.Selection) (text string, found bool) {
	if op.Length() == 0 {
		return "", false
	}
	for n := op.Nodes[0].FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode || !strings.HasPrefix(clean(nodeText(n)), "Genre") || len(clean(nodeText(n))) >= genreLabelMax {
			continue
		}
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if s.Type == html.ElementNode && hasClass(s, "bbCodeSpoiler") {
				block := goquery.NewDocumentFromNode(s).Find(".bbCodeSpoiler-content .bbCodeBlock-content").First()
				if block.Length() == 0 {
					return "", false
				}
				return blockText(block), true
			}
		}
		return "", false
	}
	return "", false
}

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

func nodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func clean(s string) string { return strings.TrimSpace(spaceRun.ReplaceAllString(s, " ")) }

// blockText renders a post fragment as text the way a browser lays it out:
// whitespace collapsed, <br> and block elements as line breaks, spoiler buttons dropped.
func blockText(sel *goquery.Selection) string {
	if sel.Length() == 0 {
		return ""
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			sb.WriteString(spaceRun.ReplaceAllString(n.Data, " "))
			return
		case html.ElementNode:
			switch n.Data {
			case "script", "style", "noscript", "button":
				return
			case "br":
				sb.WriteByte('\n')
				return
			case "div", "p", "li", "ul", "ol", "blockquote":
				sb.WriteByte('\n')
				defer sb.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(sel.Nodes[0])
	lines := strings.Split(sb.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
