package itch

import (
	"errors"
	"net/url"
	"strings"
)

// ErrNotGamePage means the link is not an http(s) itch.io game page URL.
var ErrNotGamePage = errors.New("itch: not an itch.io game page link")

// CanonicalURL normalises an itch.io game link, the one spelling shared by every
// way a Source is created. externalID is "<user>.itch.io/<slug>" lowercased
// (the key that detects an already tracked Game); gameURL is
// "https://<host>/<slug>" with the host lowercased and no query, fragment or
// trailing slash.
func CanonicalURL(raw string) (externalID, gameURL string, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", ErrNotGamePage
	}
	host := strings.ToLower(u.Hostname())
	slug := strings.Trim(u.Path, "/")
	if !strings.HasSuffix(host, ".itch.io") || len(host) == len(".itch.io") || slug == "" || strings.Contains(slug, "/") {
		return "", "", ErrNotGamePage
	}
	return host + "/" + strings.ToLower(slug), "https://" + host + "/" + slug, nil
}
