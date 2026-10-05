package itch

import (
	"errors"
	"testing"
)

func TestCanonicalURL(t *testing.T) {
	for _, c := range []struct{ in, id, url string }{
		{"https://kuro-kai.itch.io/lycoris-radiata", "kuro-kai.itch.io/lycoris-radiata", "https://kuro-kai.itch.io/lycoris-radiata"},
		{" http://Kuro-Kai.itch.io/Lycoris-Radiata/?x=1#frag ", "kuro-kai.itch.io/lycoris-radiata", "https://kuro-kai.itch.io/Lycoris-Radiata"},
		{"https://a.itch.io:443/g/", "a.itch.io/g", "https://a.itch.io/g"},
		{"https://itch.io/games", "", ""},
		{"https://user.itch.io/", "", ""},
		{"https://user.itch.io/game/devlog", "", ""},
		{"https://example.com/game", "", ""},
		{"ftp://user.itch.io/game", "", ""},
		{"not a url", "", ""},
	} {
		id, u, err := CanonicalURL(c.in)
		if c.id == "" {
			if !errors.Is(err, ErrNotGamePage) {
				t.Errorf("%q: err = %v, want ErrNotGamePage", c.in, err)
			}
			continue
		}
		if err != nil || id != c.id || u != c.url {
			t.Errorf("%q = %q, %q, %v; want %q, %q", c.in, id, u, err, c.id, c.url)
		}
	}
}
