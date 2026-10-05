// Package domain holds the typed enums, the version normalizer and the shared
// sentinel errors. Enum values mirror the CHECK constraints in migration 00001.
package domain

func in[T comparable](v T, set ...T) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

type PlayStatus string

const (
	PlayPlanned  PlayStatus = "planned"
	PlayPlaying  PlayStatus = "playing"
	PlayFinished PlayStatus = "finished"
	PlayDropped  PlayStatus = "dropped"
	PlayOnHold   PlayStatus = "on_hold"
)

func (v PlayStatus) Valid() bool {
	return in(v, PlayPlanned, PlayPlaying, PlayFinished, PlayDropped, PlayOnHold)
}

// PlatformPref is the Linux > Win/Linux > Win preference (settings and per-Game override).
type PlatformPref string

const (
	PlatformLinux    PlatformPref = "linux"
	PlatformWinLinux PlatformPref = "win_linux"
	PlatformWin      PlatformPref = "win"
)

func (v PlatformPref) Valid() bool { return in(v, PlatformLinux, PlatformWinLinux, PlatformWin) }

type SourceKind string

const (
	SourceF95Thread SourceKind = "f95_thread"
	SourceItchio    SourceKind = "itchio"
	SourceManual    SourceKind = "manual"
)

func (v SourceKind) Valid() bool { return in(v, SourceF95Thread, SourceItchio, SourceManual) }

type DevStatus string

const (
	DevOngoing   DevStatus = "ongoing"
	DevCompleted DevStatus = "completed"
	DevAbandoned DevStatus = "abandoned"
	DevOnHold    DevStatus = "on_hold"
)

func (v DevStatus) Valid() bool { return in(v, DevOngoing, DevCompleted, DevAbandoned, DevOnHold) }

type PlayLogOrigin string

const (
	PlayLogUser     PlayLogOrigin = "user"
	PlayLogImported PlayLogOrigin = "imported"
)

func (v PlayLogOrigin) Valid() bool { return in(v, PlayLogUser, PlayLogImported) }

type TagKind string

const (
	TagF95    TagKind = "f95"
	TagCustom TagKind = "custom"
)

func (v TagKind) Valid() bool { return in(v, TagF95, TagCustom) }

type SynonymOrigin string

const (
	SynonymSeed SynonymOrigin = "seed"
	SynonymUser SynonymOrigin = "user"
)

func (v SynonymOrigin) Valid() bool { return in(v, SynonymSeed, SynonymUser) }

type TagOrigin string

const (
	OriginF95List TagOrigin = "f95_list"
	OriginGenre   TagOrigin = "genre"
	OriginBoth    TagOrigin = "both"
	OriginManual  TagOrigin = "manual"
)

func (v TagOrigin) Valid() bool { return in(v, OriginF95List, OriginGenre, OriginBoth, OriginManual) }

type Qualifier string

const (
	QualPresent  Qualifier = "present"
	QualPlanned  Qualifier = "planned"
	QualOptional Qualifier = "optional"
)

func (v Qualifier) Valid() bool { return in(v, QualPresent, QualPlanned, QualOptional) }

type Verification string

const (
	VerUnverified Verification = "unverified"
	VerConfirmed  Verification = "confirmed"
	VerWrong      Verification = "wrong"
)

func (v Verification) Valid() bool { return in(v, VerUnverified, VerConfirmed, VerWrong) }

type ReviewState string

const (
	ReviewPending ReviewState = "pending"
	ReviewSkipped ReviewState = "skipped"
	ReviewDone    ReviewState = "done"
)

func (v ReviewState) Valid() bool { return in(v, ReviewPending, ReviewSkipped, ReviewDone) }

type CookieValidity string

const (
	CookieUnknown CookieValidity = "unknown"
	CookieValid   CookieValidity = "valid"
	CookieInvalid CookieValidity = "invalid"
)

func (v CookieValidity) Valid() bool { return in(v, CookieUnknown, CookieValid, CookieInvalid) }

type TokenScope string

const (
	ScopeSubmitLinks TokenScope = "submit_links"
	ScopeDownloader  TokenScope = "downloader"
)

func (v TokenScope) Valid() bool { return in(v, ScopeSubmitLinks, ScopeDownloader) }

type CheckRunKind string

const (
	RunDaily          CheckRunKind = "daily"
	RunImportBackfill CheckRunKind = "import_backfill"
	RunManual         CheckRunKind = "manual"
)

func (v CheckRunKind) Valid() bool { return in(v, RunDaily, RunImportBackfill, RunManual) }

type CheckRunStatus string

const (
	RunRunning CheckRunStatus = "running"
	RunOK      CheckRunStatus = "ok"
	RunPartial CheckRunStatus = "partial"
	RunFailed  CheckRunStatus = "failed"
)

func (v CheckRunStatus) Valid() bool { return in(v, RunRunning, RunOK, RunPartial, RunFailed) }

type CheckStep string

const (
	StepChecker     CheckStep = "checker"
	StepItchPage    CheckStep = "itch_page"
	StepDetail      CheckStep = "detail"
	StepCookieProbe CheckStep = "cookie_probe"
)

func (v CheckStep) Valid() bool { return in(v, StepChecker, StepItchPage, StepDetail, StepCookieProbe) }

type CheckOutcome string

const (
	OutcomeUnchanged CheckOutcome = "unchanged"
	OutcomeUpdate    CheckOutcome = "update"
	OutcomeMiss      CheckOutcome = "miss"
	OutcomeFetched   CheckOutcome = "fetched"
	OutcomeSkipped   CheckOutcome = "skipped"
	OutcomeError     CheckOutcome = "error"
)

func (v CheckOutcome) Valid() bool {
	return in(v, OutcomeUnchanged, OutcomeUpdate, OutcomeMiss, OutcomeFetched, OutcomeSkipped, OutcomeError)
}

type DetailReason string

const (
	ReasonAdded  DetailReason = "added"
	ReasonUpdate DetailReason = "update"
	ReasonWeekly DetailReason = "weekly"
	ReasonManual DetailReason = "manual"
	ReasonImport DetailReason = "import"
)

func (v DetailReason) Valid() bool {
	return in(v, ReasonAdded, ReasonUpdate, ReasonWeekly, ReasonManual, ReasonImport)
}

type DetailBudget string

const (
	BudgetRoutine DetailBudget = "routine"
	BudgetImport  DetailBudget = "import"
)

func (v DetailBudget) Valid() bool { return in(v, BudgetRoutine, BudgetImport) }

type NotificationKind string

const (
	NotifyDigest             NotificationKind = "digest"
	NotifyCookieInvalid      NotificationKind = "cookie_invalid"
	NotifySourceUnavailable  NotificationKind = "source_unavailable"
	NotifyDownloadNeedsHuman NotificationKind = "download_needs_human"
)

func (v NotificationKind) Valid() bool {
	return in(v, NotifyDigest, NotifyCookieInvalid, NotifySourceUnavailable, NotifyDownloadNeedsHuman)
}

type JobTrigger string

const (
	TriggerUpdate JobTrigger = "update"
	TriggerButton JobTrigger = "button"
)

func (v JobTrigger) Valid() bool { return in(v, TriggerUpdate, TriggerButton) }

type JobState string

const (
	JobAwaitingLinks JobState = "awaiting_links"
	JobQueued        JobState = "queued"
	JobDownloading   JobState = "downloading"
	JobExtracting    JobState = "extracting"
	JobDone          JobState = "done"
	JobNeedsHuman    JobState = "needs_human"
	JobCancelled     JobState = "cancelled"
)

func (v JobState) Valid() bool {
	return in(v, JobAwaitingLinks, JobQueued, JobDownloading, JobExtracting, JobDone, JobNeedsHuman, JobCancelled)
}

type MirrorPlatform string

const (
	MirrorLinux    MirrorPlatform = "linux"
	MirrorWinLinux MirrorPlatform = "win_linux"
	MirrorWin      MirrorPlatform = "win"
	MirrorOther    MirrorPlatform = "other"
)

func (v MirrorPlatform) Valid() bool {
	return in(v, MirrorLinux, MirrorWinLinux, MirrorWin, MirrorOther)
}

type MirrorState string

const (
	MirrorPending MirrorState = "pending"
	MirrorActive  MirrorState = "active"
	MirrorFailed  MirrorState = "failed"
	MirrorDone    MirrorState = "done"
	MirrorManual  MirrorState = "manual"
)

func (v MirrorState) Valid() bool {
	return in(v, MirrorPending, MirrorActive, MirrorFailed, MirrorDone, MirrorManual)
}

type TransitionActor string

const (
	ActorTracker    TransitionActor = "tracker"
	ActorDownloader TransitionActor = "downloader"
	ActorUser       TransitionActor = "user"
)

func (v TransitionActor) Valid() bool { return in(v, ActorTracker, ActorDownloader, ActorUser) }
