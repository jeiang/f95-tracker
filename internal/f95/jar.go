package f95

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/f95-tracker/internal/domain"
)

// jarNames are the only cookies the app keeps, in the order they are sent.
var jarNames = [...]string{"xf_user", "xf_tfa_trust", "xf_session", "xf_csrf"}

// Jar maps cookie name to value for the four kept cookies.
type Jar map[string]string

// ParseCookieHeader extracts the kept cookies from a pasted raw `Cookie:` header.
// It needs xf_user and either xf_tfa_trust or xf_session (R-F95-2).
func ParseCookieHeader(raw string) (Jar, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "cookie:") {
		raw = raw[7:]
	}
	jar := Jar{}
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || value == "" || !keptCookie(name) {
			continue
		}
		jar[name] = value
	}
	switch {
	case jar["xf_user"] == "":
		return nil, fmt.Errorf("%w: cookie header has no xf_user", domain.ErrValidation)
	case jar["xf_tfa_trust"] == "" && jar["xf_session"] == "":
		return nil, fmt.Errorf("%w: cookie header needs xf_tfa_trust or xf_session next to xf_user", domain.ErrValidation)
	}
	return jar, nil
}

func keptCookie(name string) bool {
	for _, n := range jarNames {
		if n == name {
			return true
		}
	}
	return false
}

// Header renders the jar as a request Cookie header value.
func (j Jar) Header() string {
	var parts []string
	for _, n := range jarNames {
		if v := j[n]; v != "" {
			parts = append(parts, n+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

func (j Jar) marshal() string {
	b, _ := json.Marshal(map[string]string(j))
	return string(b)
}

func unmarshalJar(s string) (Jar, error) {
	var j Jar
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		return nil, fmt.Errorf("f95: stored cookie jar: %w", err)
	}
	return j, nil
}
