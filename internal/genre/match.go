package genre

import (
	"regexp"
	"strings"
	"unicode"
)

// Key is the normalized lookup key: lowercase, letters a-z and digits only.
func Key(phrase string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(phrase) {
		if r < unicode.MaxASCII && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Vocabulary builds the Match vocabulary (Key(slug) → slug) from F95 tag slugs.
func Vocabulary(slugs []string) map[string]string {
	v := make(map[string]string, len(slugs))
	for _, s := range slugs {
		v[Key(s)] = s
	}
	return v
}

// Target is the tag a phrase maps to: an F95 slug, or a custom tag name.
type Target struct {
	Name   string `json:"name"`
	Custom bool   `json:"custom,omitempty"`
}

// Kind classifies how a phrase was resolved.
type Kind string

const (
	Exact   Kind = "exact"    // key equals a vocabulary slug key
	Synonym Kind = "synonym"  // synonym table, modifier strip or "a/b" compound
	NoMatch Kind = "no_match" // no tag found; Target is a custom tag named after the phrase
	Prose   Kind = "prose"    // long phrase with parentheses/prose; no Target
)

// Match is one resolved tag candidate. Compound phrases ("Anal/Vaginal") yield one Match per side.
type Match struct {
	Raw       string    `json:"raw"`
	Kind      Kind      `json:"kind"`
	Target    Target    `json:"target"` // zero for Prose
	Qualifier Qualifier `json:"qualifier"`
	Possible  bool      `json:"possible,omitempty"`
	Note      string    `json:"note,omitempty"`     // parentheticals, joined by "; "
	Modifier  string    `json:"modifier,omitempty"` // stripped leading light/mild/soft
}

// NeedsLook reports whether the Add Game page must show the match for review (R-TAG-13).
func (m Match) NeedsLook() bool { return m.Kind != Exact || m.Possible }

var modifierRe = regexp.MustCompile(`(?i)^(?:light|mild|soft|'soft')\s+`)

// Matches resolves items against vocab (Key(slug) → slug) then synonyms (key → Target),
// then collapses duplicates per R-TAG-4: one Match per target, the strongest qualifier
// wins (present, then optional, then planned; first occurrence on ties), and the
// winning item's other fields are kept. Prose matches are never collapsed.
func Matches(items []Item, vocab map[string]string, synonyms map[string]Target) []Match {
	var out []Match
	at := map[Target]int{}
	for _, it := range items {
		for _, m := range classify(it, vocab, synonyms) {
			if m.Kind == Prose {
				out = append(out, m)
				continue
			}
			k := m.Target
			if k.Custom {
				k.Name = Key(k.Name)
			}
			i, seen := at[k]
			if !seen {
				at[k] = len(out)
				out = append(out, m)
			} else if rank(m.Qualifier) > rank(out[i].Qualifier) {
				out[i] = m
			}
		}
	}
	return out
}

func rank(q Qualifier) int {
	switch q {
	case Present:
		return 2
	case Optional:
		return 1
	}
	return 0
}

func lookup(phrase string, vocab map[string]string, syn map[string]Target) (Target, Kind, bool) {
	k := Key(phrase)
	if s, ok := vocab[k]; ok {
		return Target{Name: s}, Exact, true
	}
	if t, ok := syn[k]; ok && t.Name != "" {
		return t, Synonym, true
	}
	return Target{}, NoMatch, false
}

func classify(it Item, vocab map[string]string, syn map[string]Target) []Match {
	m := Match{Raw: it.Raw, Qualifier: it.Qualifier, Possible: it.Possible, Note: strings.Join(it.Notes, "; ")}
	if it.Prose {
		m.Kind = Prose
		return []Match{m}
	}
	if t, kind, ok := lookup(it.Phrase, vocab, syn); ok {
		m.Target, m.Kind = t, kind
		return []Match{m}
	}
	if loc := modifierRe.FindString(it.Phrase); loc != "" {
		if t, _, ok := lookup(it.Phrase[len(loc):], vocab, syn); ok {
			m.Target, m.Kind = t, Synonym
			m.Modifier = strings.ToLower(strings.Trim(strings.TrimSpace(loc), "'"))
			return []Match{m}
		}
	}
	if strings.Contains(it.Phrase, "/") {
		var out []Match
		for _, part := range strings.Split(it.Phrase, "/") {
			t, _, ok := lookup(part, vocab, syn)
			if !ok {
				out = nil
				break
			}
			c := m
			c.Target, c.Kind = t, Synonym
			out = append(out, c)
		}
		if out != nil {
			return out
		}
	}
	m.Kind, m.Target = NoMatch, Target{Name: it.Phrase, Custom: true}
	return []Match{m}
}

// F95Only returns the F95 tag slugs (in the given order) that no Genre match produced:
// the thread's F95 list minus the Genre text's targets, at any qualifier (R-TAG-5).
func F95Only(matches []Match, f95Slugs []string) []string {
	in := map[string]bool{}
	for _, m := range matches {
		if m.Kind != Prose && !m.Target.Custom {
			in[m.Target.Name] = true
		}
	}
	var out []string
	for _, s := range f95Slugs {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}
