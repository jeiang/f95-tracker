package ui

import "github.com/jeiang/f95-tracker/internal/domain"

// SettingsView is the /settings page model.
type SettingsView struct {
	Cookie   CookieView
	Alert    AlertView
	Synonyms SynonymsView
	Ntfy     NtfyView
}

// CookieView is the F95 cookie status. The cookie value itself is never part of it.
type CookieView struct {
	Stored      bool
	Validity    string // valid, invalid or unknown
	CheckedAt   string // last successful check, display text
	TfaExpires  string // YYYY-MM-DD, "" = unknown
	Message     string // outcome of the last save, "" = none
	MessageTone string // ok, warn or bad
}

// AlertRow is one Play status in the alert set.
type AlertRow struct {
	Status  domain.PlayStatus
	Count   int64
	Checked bool
}

// AlertView is the Update alert Play-status set with affected Game counts.
type AlertView struct {
	Rows     []AlertRow
	Affected int64
	Total    int64
	Saved    bool
}

// SynonymRow is one Synonym; Target is the tag slug (F95 tag) or label (Custom tag).
type SynonymRow struct {
	ID     int64
	Phrase string
	Target string
	Kind   string // f95 or custom
}

// SynonymOption is a tag the target field suggests.
type SynonymOption struct {
	Value string
	Label string
}

// SynonymsView is the Synonym table with its filter and the last edit's outcome.
type SynonymsView struct {
	Filter  string
	Rows    []SynonymRow
	Options []SynonymOption
	Message string
}

// NtfyView shows the deploy-time ntfy settings read-only plus the last test outcome.
type NtfyView struct {
	Enabled bool
	URL     string
	Topic   string
	Tested  bool
	Err     string // "" with Tested = success
}
