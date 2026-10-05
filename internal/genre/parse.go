// Package genre parses the free-text "Genre" line of an F95 thread (and pasted
// itch.io / manual tag text) into tag candidates. It is pure: no DB, no network.
// Rules follow docs/research/f95-genre-text.md (R-TAG-3).
package genre

import (
	"regexp"
	"strings"
)

// Qualifier is how a tag applies to a Game (R-TAG-5).
type Qualifier string

const (
	Present  Qualifier = "present"
	Planned  Qualifier = "planned"
	Optional Qualifier = "optional"
)

// Item is one phrase extracted from the text, before any vocabulary lookup.
type Item struct {
	Raw       string    `json:"raw"`    // phrase as written, markers and parentheses kept
	Phrase    string    `json:"phrase"` // Raw without optional markers, parentheticals and whitespace noise
	Qualifier Qualifier `json:"qualifier"`
	Possible  bool      `json:"possible,omitempty"` // under a "Possible…:" heading: planned, needs confirmation
	Notes     []string  `json:"notes,omitempty"`    // other parentheticals, e.g. "soft" in "Vore (soft)"
	Prose     bool      `json:"prose,omitempty"`    // long phrase with parentheses/prose: manual handling
}

const optWord = `(?:optional|toggleable|avoidable|can be disabled)`

var (
	spoilerLine = regexp.MustCompile(`(?i)^\[\[/?spoiler[^\]]*\]\]$`)
	headRe      = regexp.MustCompile(`(?i)^((?:planned|future|possible|currently)\b[^:\n]*?)\s*:\s*(.*)$`)
	bulletRe    = regexp.MustCompile(`^[-*\x{2022}]\s*`)
	forNowRe    = regexp.MustCompile(`(?i)\bfor now\b`)
	optParen    = regexp.MustCompile(`(?i)\(\s*` + optWord + `\s*\)`)
	optPrefix   = regexp.MustCompile(`(?i)^\s*` + optWord + `\s+`)
	parenRe     = regexp.MustCompile(`\(([^)]*)\)`)
	spaceRe     = regexp.MustCompile(`\s+`)
	proseRe     = regexp.MustCompile(`[\p{L}\p{N}_]\([^)]*\)|\b(?:is|are|etc|as)\b`)
)

// Parse splits text into items. The section (present/planned/possible) set by a
// heading persists across blank lines and [[spoiler]] wrappers until the next heading.
func Parse(text string) []Item {
	var items []Item
	section, possible := Present, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || spoilerLine.MatchString(line) {
			continue
		}
		if m := headRe.FindStringSubmatch(line); m != nil {
			section, possible = Present, false
			switch h := strings.ToLower(m[1]); {
			case strings.HasPrefix(h, "planned"), strings.HasPrefix(h, "future"):
				section = Planned
			case strings.HasPrefix(h, "currently"):
			default:
				section, possible = Planned, true
			}
			line = strings.TrimSpace(m[2])
			if line == "" {
				continue
			}
		}
		line = bulletRe.ReplaceAllString(line, "")
		for _, tok := range tokenize(line) {
			if it, ok := parseToken(tok, section, possible); ok {
				items = append(items, it)
			}
		}
	}
	return items
}

func parseToken(tok string, section Qualifier, possible bool) (Item, bool) {
	raw := strings.TrimSpace(strings.Trim(strings.TrimSpace(tok), "."))
	if forNowRe.MatchString(raw) {
		raw = strings.TrimSpace(forNowRe.ReplaceAllString(raw, ""))
	}
	if raw == "" {
		return Item{}, false
	}
	it := Item{Raw: raw, Qualifier: section, Possible: possible}
	base := raw
	if optParen.MatchString(base) {
		it.Qualifier = Optional
		base = optParen.ReplaceAllString(base, " ")
	}
	if optPrefix.MatchString(base) {
		it.Qualifier = Optional
		base = optPrefix.ReplaceAllString(base, "")
	}
	for _, p := range parenRe.FindAllStringSubmatch(base, -1) {
		it.Notes = append(it.Notes, strings.TrimSpace(p[1]))
	}
	base = parenRe.ReplaceAllString(base, " ")
	it.Phrase = strings.TrimSpace(spaceRe.ReplaceAllString(base, " "))
	if proseRe.MatchString(raw) && len(strings.Fields(raw)) > 5 {
		it.Prose = true
		it.Qualifier = section
	}
	return it, true
}

// tokenize splits on commas, sentence periods (followed by space, end or another
// period) and ellipses, never inside parentheses.
func tokenize(line string) []string {
	r := []rune(line)
	var out []string
	var cur []rune
	depth := 0
	for i, ch := range r {
		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
		sep := false
		if depth == 0 {
			switch ch {
			case ',':
				sep = true
			case '.':
				sep = i+1 >= len(r) || r[i+1] == ' ' || r[i+1] == '.' || (i > 0 && r[i-1] == '.')
			}
		}
		if sep {
			out = append(out, string(cur))
			cur = cur[:0]
		} else {
			cur = append(cur, ch)
		}
	}
	return append(out, string(cur))
}
