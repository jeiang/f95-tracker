package domain

// NormalizeVersion mirrors the SQL latest_version_norm / version_norm expression
// byte for byte: trim space, tab, LF, CR; strip one leading v/V; ASCII lower-case.
// Non-ASCII bytes are untouched (SQLite lower() is ASCII-only).
func NormalizeVersion(s string) string {
	const ws = " \t\n\r"
	start, end := 0, len(s)
	for start < end && isWS(s[start], ws) {
		start++
	}
	for end > start && isWS(s[end-1], ws) {
		end--
	}
	s = s[start:end]
	if s != "" && (s[0] == 'v' || s[0] == 'V') {
		s = s[1:]
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; 'A' <= c && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func isWS(c byte, ws string) bool {
	for i := 0; i < len(ws); i++ {
		if ws[i] == c {
			return true
		}
	}
	return false
}
