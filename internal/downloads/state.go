package downloads

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// State is a download job state (R-DL-2).
type State string

const (
	AwaitingLinks State = "awaiting_links"
	Queued        State = "queued"
	Downloading   State = "downloading"
	Extracting    State = "extracting"
	Done          State = "done"
	NeedsHuman    State = "needs_human"
	Cancelled     State = "cancelled"
)

// Terminal reports whether no transition leaves s.
func (s State) Terminal() bool { return s == Done || s == Cancelled }

func (s State) valid() bool {
	return slices.Contains([]State{AwaitingLinks, Queued, Downloading, Extracting, Done, NeedsHuman, Cancelled}, s)
}

// Actor is who caused a transition, as stored in download_job_transition.actor.
// ActorTracker is the "system" actor: the tracker itself acting without a user request.
type Actor string

const (
	ActorUser       Actor = "user"
	ActorDownloader Actor = "downloader"
	ActorTracker    Actor = "tracker"
)

type edge struct{ from, to State }

// edges is the whole state machine: which actors may take each edge.
var edges = func() map[edge][]Actor {
	m := map[edge][]Actor{
		{AwaitingLinks, Queued}:   {ActorUser, ActorTracker},
		{Queued, Downloading}:     {ActorDownloader},
		{Downloading, Extracting}: {ActorDownloader},
		{Extracting, Done}:        {ActorDownloader},
		{NeedsHuman, Done}:        {ActorUser},
		{NeedsHuman, Queued}:      {ActorUser},
	}
	for _, from := range []State{Queued, Downloading, Extracting} {
		m[edge{from, Queued}] = []Actor{ActorDownloader} // mirror fall-through
	}
	for _, from := range []State{AwaitingLinks, Queued, Downloading, Extracting} {
		m[edge{from, NeedsHuman}] = []Actor{ActorUser, ActorDownloader, ActorTracker}
	}
	for _, from := range []State{AwaitingLinks, Queued, Downloading, Extracting, NeedsHuman} {
		m[edge{from, Cancelled}] = []Actor{ActorUser}
	}
	return m
}()

// Allowed reports whether actor may move a job from one state to another.
func Allowed(from, to State, actor Actor) bool {
	return slices.Contains(edges[edge{from, to}], actor)
}

// Transition is the only function that changes download_job.state (R-DL-2). It checks the edge,
// writes the new state and the download_job_transition row through q, and returns the previous
// state. detail is the audit text; for NeedsHuman it is also the job's needs_human_reason and
// must not be empty. Callers pass the Queries of their own transaction.
func Transition(ctx context.Context, q *sqlcgen.Queries, at time.Time, jobID int64, to State, actor Actor, detail string) (State, error) {
	job, err := q.GetDownloadJob(ctx, jobID)
	if err != nil {
		return "", notFound(err)
	}
	from := State(job.State)
	if !Allowed(from, to, actor) {
		return from, fmt.Errorf("%w: %s -> %s by %s", domain.ErrIllegalTransition, from, to, actor)
	}
	reason := nullStr("")
	if to == NeedsHuman {
		if detail == "" {
			return from, fmt.Errorf("%w: needs_human requires a reason", domain.ErrValidation)
		}
		reason = nullStr(detail)
	}
	now := clock.Timestamp(at)
	if err := q.SetDownloadJobState(ctx, sqlcgen.SetDownloadJobStateParams{State: string(to), NeedsHumanReason: reason, UpdatedAt: now, ID: jobID}); err != nil {
		return from, err
	}
	err = q.InsertDownloadTransition(ctx, sqlcgen.InsertDownloadTransitionParams{
		JobID: jobID, FromState: nullStr(string(from)), ToState: string(to), Actor: string(actor), Detail: nullStr(detail), At: now,
	})
	return from, err
}
