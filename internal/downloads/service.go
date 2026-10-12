// Package downloads is the tracker side of the downloader (R-DL-1..5): jobs, their ordered
// mirrors, the state machine, and the actions users and the worker take on them.
package downloads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/notify"
)

const (
	// HistoryWindow is how long cancelled jobs stay visible to the worker and finished jobs on the page.
	HistoryWindow = 7 * 24 * time.Hour

	ReasonNoSupportedMirror = "no supported mirror"
	ReasonAllMirrorsFailed  = "all mirrors failed"
)

// Trigger is what created a job.
type Trigger string

const (
	TriggerUpdate Trigger = "update"
	TriggerButton Trigger = "button"
)

// Mirror is one link of a job.
type Mirror struct {
	ID        int64
	Position  int
	Host, URL string
	Platform  string // "" = unknown
	Supported bool
	State     string // pending, active, failed, done or manual
	Error     string
	Attempts  int
}

// Job is a download job with its Game and mirrors.
type Job struct {
	ID, GameID, SourceID  int64
	GameName              string
	TargetVersion         string
	Trigger               Trigger
	State                 State
	NeedsHumanReason      string
	ActiveMirrorID        int64 // 0 = none
	BytesDone, BytesTotal *int64
	ResultPath            string
	// EffectivePlatformPref is the Game's override, else the default (R-DL-4).
	EffectivePlatformPref domain.PlatformPref
	CreatedAt, UpdatedAt  string
	Mirrors               []Mirror
}

// Service creates jobs and applies actions to them.
type Service struct {
	store  *db.Store
	clock  clock.Clock
	notify *notify.Notifier // nil = no pushes
}

func New(store *db.Store, clk clock.Clock, n *notify.Notifier) *Service {
	return &Service{store: store, clock: clk, notify: n}
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: download job", domain.ErrNotFound)
	}
	return err
}

// NewJob describes a job to create.
type NewJob struct {
	GameID, SourceID int64
	Version          string
	Trigger          Trigger
	Actor            Actor
}

// EnsureJob returns the open job for (Game, version), creating an awaiting_links one when there
// is none (R-DL-1, INV-26). Callers pass the Queries of their own transaction.
func EnsureJob(ctx context.Context, q *sqlcgen.Queries, at time.Time, p NewJob) (job sqlcgen.DownloadJob, created bool, err error) {
	job, err = q.GetOpenDownloadJob(ctx, sqlcgen.GetOpenDownloadJobParams{GameID: p.GameID, TargetVersion: p.Version})
	if err == nil {
		return job, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return job, false, err
	}
	now := clock.Timestamp(at)
	job, err = q.InsertDownloadJob(ctx, sqlcgen.InsertDownloadJobParams{
		GameID: p.GameID, SourceID: p.SourceID, TargetVersion: p.Version, Trigger: string(p.Trigger), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return job, false, err
	}
	err = q.InsertDownloadTransition(ctx, sqlcgen.InsertDownloadTransitionParams{
		JobID: job.ID, ToState: string(AwaitingLinks), Actor: string(p.Actor), Detail: nullStr(string(p.Trigger)), At: now,
	})
	return job, true, err
}

// Create is the Download button: the open job for the Game's latest version, created when
// missing. created reports whether it was new.
func (s *Service) Create(ctx context.Context, gameID int64) (job Job, created bool, err error) {
	var id int64
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		src, err := q.GetPrimarySource(ctx, gameID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: game", domain.ErrNotFound)
		}
		if err != nil {
			return err
		}
		if !src.LatestVersion.Valid {
			return fmt.Errorf("%w: the Game has no known version yet", domain.ErrValidation)
		}
		j, c, err := EnsureJob(ctx, q, s.clock.Now(), NewJob{GameID: gameID, SourceID: src.ID, Version: src.LatestVersion.String, Trigger: TriggerButton, Actor: ActorUser})
		id, created = j.ID, c
		return err
	})
	if err != nil {
		return Job{}, false, err
	}
	job, err = s.Get(ctx, id)
	return job, created, err
}

func (s *Service) defaultPref(ctx context.Context, q *sqlcgen.Queries) (domain.PlatformPref, error) {
	st, err := q.GetSettings(ctx)
	return domain.PlatformPref(st.PlatformPref), err
}

func (s *Service) jobFromRow(j sqlcgen.DownloadJob, name string, gamePref sql.NullString, def domain.PlatformPref, mirrors []sqlcgen.DownloadMirror) Job {
	out := Job{
		ID: j.ID, GameID: j.GameID, SourceID: j.SourceID, GameName: name, TargetVersion: j.TargetVersion,
		Trigger: Trigger(j.Trigger), State: State(j.State), NeedsHumanReason: j.NeedsHumanReason.String,
		ActiveMirrorID: j.ActiveMirrorID.Int64, ResultPath: j.ResultPath.String, EffectivePlatformPref: def,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
	if gamePref.Valid {
		out.EffectivePlatformPref = domain.PlatformPref(gamePref.String)
	}
	if j.BytesDone.Valid {
		out.BytesDone = &j.BytesDone.Int64
	}
	if j.BytesTotal.Valid {
		out.BytesTotal = &j.BytesTotal.Int64
	}
	for _, m := range mirrors {
		out.Mirrors = append(out.Mirrors, mirrorOf(m))
	}
	return out
}

func mirrorOf(m sqlcgen.DownloadMirror) Mirror {
	return Mirror{
		ID: m.ID, Position: int(m.Position), Host: m.Host, URL: m.Url, Platform: m.Platform.String,
		Supported: m.Supported == 1, State: m.State, Error: m.Error.String, Attempts: int(m.Attempts),
	}
}

func (s *Service) loadJob(ctx context.Context, q *sqlcgen.Queries, id int64) (Job, error) {
	r, err := q.DownloadJobRow(ctx, id)
	if err != nil {
		return Job{}, notFound(err)
	}
	def, err := s.defaultPref(ctx, q)
	if err != nil {
		return Job{}, err
	}
	ms, err := q.ListDownloadMirrors(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return s.jobFromRow(r.DownloadJob, r.GameName, r.GamePlatformPref, def, ms), nil
}

// Get returns one job with its mirrors, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (Job, error) {
	return s.loadJob(ctx, s.store.Queries(), id)
}

func (s *Service) attachMirrors(ctx context.Context, q *sqlcgen.Queries, jobs []Job) error {
	if len(jobs) == 0 {
		return nil
	}
	ids := make([]int64, len(jobs))
	idx := map[int64]int{}
	for i, j := range jobs {
		ids[i], idx[j.ID] = j.ID, i
	}
	ms, err := q.ListDownloadMirrorsForJobs(ctx, ids)
	if err != nil {
		return err
	}
	for _, m := range ms {
		j := &jobs[idx[m.JobID]]
		j.Mirrors = append(j.Mirrors, mirrorOf(m))
	}
	return nil
}

// List returns the jobs for the downloads page: open ones first, then finished or cancelled
// ones from the last HistoryWindow.
func (s *Service) List(ctx context.Context) ([]Job, error) {
	q := s.store.Queries()
	def, err := s.defaultPref(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRecentDownloadJobRows(ctx, clock.Timestamp(s.clock.Now().Add(-HistoryWindow)))
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, len(rows))
	for i, r := range rows {
		jobs[i] = s.jobFromRow(r.DownloadJob, r.GameName, r.GamePlatformPref, def, nil)
	}
	return jobs, s.attachMirrors(ctx, q, jobs)
}

// WorkerJobs returns what the worker acts on: queued, downloading and extracting jobs, and the
// ids of jobs cancelled within HistoryWindow.
func (s *Service) WorkerJobs(ctx context.Context) (jobs []Job, cancelled []int64, err error) {
	q := s.store.Queries()
	def, err := s.defaultPref(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.ListDownloadJobRows(ctx, []string{string(Queued), string(Downloading), string(Extracting)})
	if err != nil {
		return nil, nil, err
	}
	jobs = make([]Job, len(rows))
	for i, r := range rows {
		jobs[i] = s.jobFromRow(r.DownloadJob, r.GameName, r.GamePlatformPref, def, nil)
	}
	if err := s.attachMirrors(ctx, q, jobs); err != nil {
		return nil, nil, err
	}
	cancelled, err = q.ListCancelledDownloadJobIDs(ctx, clock.Timestamp(s.clock.Now().Add(-HistoryWindow)))
	return jobs, cancelled, err
}

// push sends the once-per-job "Download needs you" push for jobs that entered needs_human.
// Delivery failures are recorded by the notifier and retried by the next check run.
func (s *Service) push(ctx context.Context, jobID int64) {
	if s.notify != nil {
		_ = s.notify.DownloadNeedsYou(ctx, jobID)
	}
}

// act runs one user action on a job in a transaction.
func (s *Service) act(ctx context.Context, id int64, fn func(q *sqlcgen.Queries) error) (Job, error) {
	if err := s.store.WithTx(ctx, fn); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}

// Cancel is the user action cancel (the worker stops on its next poll).
func (s *Service) Cancel(ctx context.Context, id int64) (Job, error) {
	return s.act(ctx, id, func(q *sqlcgen.Queries) error {
		_, err := Transition(ctx, q, s.clock.Now(), id, Cancelled, ActorUser, "")
		return err
	})
}

// MarkDone is the user action done: a needs_human job the user finished by hand.
func (s *Service) MarkDone(ctx context.Context, id int64) (Job, error) {
	return s.act(ctx, id, func(q *sqlcgen.Queries) error {
		_, err := Transition(ctx, q, s.clock.Now(), id, Done, ActorUser, "")
		return err
	})
}

// Retry is the user action retry: failed mirrors become pending and the job is queued again.
// It needs at least one supported mirror (paste links first otherwise).
func (s *Service) Retry(ctx context.Context, id int64) (Job, error) {
	return s.act(ctx, id, func(q *sqlcgen.Queries) error {
		n, err := q.CountSupportedMirrors(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: the job has no supported mirror; paste links first", domain.ErrConflict)
		}
		if _, err := Transition(ctx, q, s.clock.Now(), id, Queued, ActorUser, "retry"); err != nil {
			return err
		}
		return q.ResetFailedDownloadMirrors(ctx, id)
	})
}

// IngestResult is the outcome of storing submitted links.
type IngestResult struct {
	Job      Job
	Accepted int // distinct links stored
	Manual   int // of those, the ones kept as manual links (unsupported host)
}

// IngestLinks stores links on a job awaiting links or needing a human, replacing its mirror
// list, and moves it on per R-DL-5: queued with a supported mirror, else needs_human ("no
// supported mirror", one push). Masked URLs fail the whole call with ErrMasked.
func (s *Service) IngestLinks(ctx context.Context, jobID int64, in []LinkInput) (IngestResult, error) {
	links, err := ClassifyAll(in)
	if err != nil {
		return IngestResult{}, err
	}
	var res IngestResult
	var toNeedsHuman bool
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		res, toNeedsHuman, err = s.ingest(ctx, q, jobID, links)
		return err
	})
	if err != nil {
		return IngestResult{}, err
	}
	if toNeedsHuman {
		s.push(ctx, jobID)
	}
	res.Job, err = s.Get(ctx, jobID)
	return res, err
}

func (s *Service) ingest(ctx context.Context, q *sqlcgen.Queries, jobID int64, links []Link) (res IngestResult, toNeedsHuman bool, err error) {
	job, err := q.DownloadJobRow(ctx, jobID)
	if err != nil {
		return res, false, notFound(err)
	}
	state := State(job.DownloadJob.State)
	if state != AwaitingLinks && state != NeedsHuman {
		return res, false, fmt.Errorf("%w: job is %s, not waiting for links", domain.ErrConflict, state)
	}
	st, err := q.GetSettings(ctx)
	if err != nil {
		return res, false, err
	}
	var hostOrder []string
	if err := json.Unmarshal([]byte(st.MirrorHostOrder), &hostOrder); err != nil {
		return res, false, err
	}
	pref := st.PlatformPref
	if job.GamePlatformPref.Valid {
		pref = job.GamePlatformPref.String
	}
	ordered := Order(links, pref, hostOrder)
	if err := q.DeleteDownloadMirrors(ctx, jobID); err != nil {
		return res, false, err
	}
	supported := 0
	for i, l := range ordered {
		ms := "manual"
		sup := int64(0)
		if l.Supported {
			ms, sup = "pending", 1
			supported++
		}
		if err := q.InsertDownloadMirror(ctx, sqlcgen.InsertDownloadMirrorParams{
			JobID: jobID, Position: int64(i), Host: l.Host, Url: l.URL, Platform: nullStr(l.Platform), Supported: sup, State: ms,
		}); err != nil {
			return res, false, err
		}
	}
	res.Accepted, res.Manual = len(ordered), len(ordered)-supported
	switch {
	case supported > 0:
		_, err = Transition(ctx, q, s.clock.Now(), jobID, Queued, ActorUser, "links submitted")
	case state == AwaitingLinks:
		_, err = Transition(ctx, q, s.clock.Now(), jobID, NeedsHuman, ActorUser, ReasonNoSupportedMirror)
		toNeedsHuman = true
	}
	return res, toNeedsHuman, err
}

// PendingForThread returns the job awaiting links for the Game owning an F95 thread, or
// domain.ErrNotFound.
func (s *Service) PendingForThread(ctx context.Context, threadID string) (Job, error) {
	q := s.store.Queries()
	th, err := q.FindThreadGame(ctx, nullStr(threadID))
	if err != nil {
		return Job{}, fmt.Errorf("%w: thread %s is not tracked", domain.ErrNotFound, threadID)
	}
	open, err := q.ListOpenDownloadJobsForGame(ctx, th.GameID)
	if err != nil {
		return Job{}, err
	}
	for _, j := range open {
		if State(j.State) == AwaitingLinks {
			return s.Get(ctx, j.ID)
		}
	}
	return Job{}, fmt.Errorf("%w: no job awaits links for thread %s", domain.ErrNotFound, threadID)
}

// SubmitLinks is the userscript hand-off: links for an F95 thread go to the Game's job awaiting
// links (or needing a human). With no open job one is created for the Game's latest version
// (trigger button, R-DL-5). Unknown thread: domain.ErrNotFound; an open job in another state:
// domain.ErrConflict.
func (s *Service) SubmitLinks(ctx context.Context, threadID string, in []LinkInput) (IngestResult, error) {
	links, err := ClassifyAll(in)
	if err != nil {
		return IngestResult{}, err
	}
	var res IngestResult
	var jobID int64
	var toNeedsHuman bool
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		th, err := q.FindThreadGame(ctx, nullStr(threadID))
		if err != nil {
			return fmt.Errorf("%w: thread %s is not tracked", domain.ErrNotFound, threadID)
		}
		open, err := q.ListOpenDownloadJobsForGame(ctx, th.GameID)
		if err != nil {
			return err
		}
		jobID = 0
		for _, j := range open {
			if st := State(j.State); st == AwaitingLinks || st == NeedsHuman {
				jobID = j.ID
				break
			}
		}
		switch {
		case jobID != 0:
		case len(open) > 0:
			return fmt.Errorf("%w: the Game's download is already %s", domain.ErrConflict, open[0].State)
		default:
			src, err := q.GetPrimarySource(ctx, th.GameID)
			if err != nil {
				return err
			}
			if !src.LatestVersion.Valid {
				return fmt.Errorf("%w: the Game has no known version yet", domain.ErrValidation)
			}
			j, _, err := EnsureJob(ctx, q, s.clock.Now(), NewJob{GameID: th.GameID, SourceID: th.SourceID, Version: src.LatestVersion.String, Trigger: TriggerButton, Actor: ActorUser})
			if err != nil {
				return err
			}
			jobID = j.ID
		}
		res, toNeedsHuman, err = s.ingest(ctx, q, jobID, links)
		return err
	})
	if err != nil {
		return IngestResult{}, err
	}
	if toNeedsHuman {
		s.push(ctx, jobID)
	}
	res.Job, err = s.Get(ctx, jobID)
	return res, err
}

// Event is a worker report on a job (R-DL-6). Pointer and empty fields are absent.
type Event struct {
	State            State
	ActiveMirrorID   *int64
	MirrorID         *int64
	MirrorState      string // active, failed or done
	Error            string
	BytesDone        *int64
	BytesTotal       *int64
	NeedsHumanReason string
	ResultPath       string
}

// workerStates are the states a worker may report.
var workerStates = []State{Downloading, Extracting, Done, Queued, NeedsHuman}

// ApplyEvent records a worker report in one transaction and returns the job. A state the worker
// repeats (downloading, extracting) is just progress. When the report fails the last supported
// mirror, the job moves to needs_human ("all mirrors failed") whatever state was reported, and
// the single push goes out. Illegal moves return domain.ErrIllegalTransition.
func (s *Service) ApplyEvent(ctx context.Context, jobID int64, ev Event) (Job, error) {
	if ev.State != "" && !ev.State.valid() {
		return Job{}, fmt.Errorf("%w: unknown state %q", domain.ErrValidation, ev.State)
	}
	switch ev.MirrorState {
	case "", "active", "failed", "done":
	default:
		return Job{}, fmt.Errorf("%w: unknown mirror_state %q", domain.ErrValidation, ev.MirrorState)
	}
	if (ev.MirrorID == nil) != (ev.MirrorState == "") {
		return Job{}, fmt.Errorf("%w: mirror_id and mirror_state go together", domain.ErrValidation)
	}
	if ev.State == NeedsHuman && ev.NeedsHumanReason == "" {
		return Job{}, fmt.Errorf("%w: needs_human_reason is required", domain.ErrValidation)
	}
	toNeedsHuman := false
	err := s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		toNeedsHuman = false
		job, err := q.GetDownloadJob(ctx, jobID)
		if err != nil {
			return notFound(err)
		}
		cur := State(job.State)
		if cur.Terminal() {
			return fmt.Errorf("%w: job is %s", domain.ErrIllegalTransition, cur)
		}
		now := s.clock.Now()
		active := ev.ActiveMirrorID
		allFailed := false
		if ev.MirrorID != nil {
			m, err := q.GetDownloadMirror(ctx, *ev.MirrorID)
			if err != nil || m.JobID != jobID {
				return fmt.Errorf("%w: mirror %d of job %d", domain.ErrNotFound, *ev.MirrorID, jobID)
			}
			if m.Supported == 0 {
				return fmt.Errorf("%w: mirror %d is a manual link", domain.ErrValidation, m.ID)
			}
			attempt := int64(0)
			if ev.MirrorState == "active" {
				attempt = 1
				active = ev.MirrorID
			}
			if err := q.SetDownloadMirrorState(ctx, sqlcgen.SetDownloadMirrorStateParams{State: ev.MirrorState, Error: nullStr(ev.Error), Attempts: attempt, ID: m.ID}); err != nil {
				return err
			}
			if ev.MirrorState == "failed" {
				left, err := q.CountUnfailedSupportedMirrors(ctx, jobID)
				if err != nil {
					return err
				}
				allFailed = left == 0
			}
		}
		if err := q.SetDownloadJobProgress(ctx, sqlcgen.SetDownloadJobProgressParams{
			ActiveMirrorID: nullInt(active), BytesDone: nullInt(ev.BytesDone), BytesTotal: nullInt(ev.BytesTotal),
			ResultPath: nullStr(ev.ResultPath), UpdatedAt: clock.Timestamp(now), ID: jobID,
		}); err != nil {
			return err
		}
		to, reason := ev.State, ev.NeedsHumanReason
		if allFailed && cur != NeedsHuman && to != NeedsHuman {
			to, reason = NeedsHuman, ReasonAllMirrorsFailed
		}
		if to == "" || (to == cur && (to == Downloading || to == Extracting)) {
			return nil
		}
		if !slices.Contains(workerStates, to) {
			return fmt.Errorf("%w: the worker may not report %s", domain.ErrIllegalTransition, to)
		}
		if _, err := Transition(ctx, q, now, jobID, to, ActorDownloader, reason); err != nil {
			return err
		}
		toNeedsHuman = to == NeedsHuman
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	if toNeedsHuman {
		s.push(ctx, jobID)
	}
	return s.Get(ctx, jobID)
}
