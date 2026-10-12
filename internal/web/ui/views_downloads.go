package ui

import "strconv"

// MirrorRow is one mirror of a download job.
type MirrorRow struct {
	Position  int
	Host      string
	URL       string
	Platform  string // display text, "" = unknown
	Supported bool
	State     string
	Error     string
	Active    bool
}

// DownloadJobRow is one job on /downloads.
type DownloadJobRow struct {
	ID       int64
	GameID   int64
	GameName string
	Version  string
	State    string // data-model value
	Reason   string // needs_human reason
	Trigger  string // "update" or "button"
	Progress string // "12.3 MB of 40.0 MB", "" = unknown
	Result   string // result path for done jobs
	Updated  string
	Mirrors  []MirrorRow

	CanPaste  bool
	CanCancel bool
	CanDone   bool
	CanRetry  bool
}

// DownloadsView is the /downloads page model.
type DownloadsView struct {
	Jobs    []DownloadJobRow
	Message string // outcome of the last action
}

// PasteLinksView is the /downloads/{id}/links page model. Links is the textarea content kept
// across a rejected submission.
type PasteLinksView struct {
	JobID    int64
	GameID   int64
	GameName string
	Version  string
	State    string
	Links    string
	Platform string // select value, "" = guess from the line
	Error    string
	Mirrors  []MirrorRow // links already on the job
}

// DownloadPanel is the Download section of Game detail. Version "" = no known version.
type DownloadPanel struct {
	Version      string
	JobHref      string // open job for Version: its paste page or the downloads list; "" = none
	JobState     string
	PlatformPref string // per-Game override, "" = use the default
	DefaultPref  string // the default's value, shown in the select
}

// DownloadPrefsView is the downloader preferences form of Settings.
type DownloadPrefsView struct {
	HostOrder    string // comma separated, e.g. "pixeldrain, mega, gofile"
	PlatformPref string
	Saved        bool
}

// TokenRow is one API token; the plaintext is never part of it.
type TokenRow struct {
	ID       int64
	Name     string
	Scope    string
	Created  string
	LastUsed string // "" = never
}

// NewToken is a freshly created token; Plaintext is shown this once.
type NewToken struct {
	Name      string
	Plaintext string
}

// TokensView is the API token section of Settings.
type TokensView struct {
	Rows    []TokenRow
	New     *NewToken
	Message string
}

// PlatformOption is one choice of a platform preference select.
type PlatformOption struct{ Value, Label string }

// PlatformPrefs are the platform preference values in default order.
var PlatformPrefs = []PlatformOption{
	{"linux", "Linux"}, {"win_linux", "Win/Linux"}, {"win", "Win"},
}

// PlatformPrefLabel is the UI text for a platform preference value.
func PlatformPrefLabel(v string) string {
	for _, o := range PlatformPrefs {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}

func jobHref(id int64, rest string) string { return "/downloads/" + strconv.FormatInt(id, 10) + rest }

// downloadStateTone and Label map a job state to its chip.
func downloadStateTone(state string) string {
	switch state {
	case "done":
		return "ok"
	case "needs_human":
		return "bad"
	case "awaiting_links":
		return "warn"
	case "cancelled":
		return "muted"
	}
	return "info"
}

func downloadStateLabel(state string) string {
	switch state {
	case "awaiting_links":
		return "Awaiting links"
	case "needs_human":
		return "Needs you"
	case "queued":
		return "Queued"
	case "downloading":
		return "Downloading"
	case "extracting":
		return "Extracting"
	case "done":
		return "Done"
	case "cancelled":
		return "Cancelled"
	}
	return state
}

var mirrorStateLabels = map[string]string{"pending": "pending", "active": "downloading", "failed": "failed", "done": "done", "manual": "manual link"}
