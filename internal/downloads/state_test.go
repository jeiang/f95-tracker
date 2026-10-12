package downloads

import (
	"errors"
	"testing"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

var allStates = []State{AwaitingLinks, Queued, Downloading, Extracting, Done, NeedsHuman, Cancelled}
var allActors = []Actor{ActorUser, ActorDownloader, ActorTracker}

// legal is R-DL-2 written out edge by edge; every other (from, to, actor) must be rejected.
var legal = map[[3]string]bool{
	{"awaiting_links", "queued", "user"}:        true,
	{"awaiting_links", "queued", "tracker"}:     true,
	{"queued", "downloading", "downloader"}:     true,
	{"downloading", "extracting", "downloader"}: true,
	{"extracting", "done", "downloader"}:        true,
	{"needs_human", "done", "user"}:             true,
	{"needs_human", "queued", "user"}:           true,
}

func init() {
	for _, from := range []State{Queued, Downloading, Extracting} {
		legal[[3]string{string(from), "queued", "downloader"}] = true // fall-through
	}
	for _, from := range []State{AwaitingLinks, Queued, Downloading, Extracting} {
		for _, a := range allActors {
			legal[[3]string{string(from), "needs_human", string(a)}] = true
		}
	}
	for _, from := range []State{AwaitingLinks, Queued, Downloading, Extracting, NeedsHuman} {
		legal[[3]string{string(from), "cancelled", "user"}] = true
	}
}

func TestStateMachineEveryEdge(t *testing.T) {
	for _, from := range allStates {
		for _, to := range allStates {
			for _, a := range allActors {
				want := legal[[3]string{string(from), string(to), string(a)}]
				if got := Allowed(from, to, a); got != want {
					t.Errorf("%s -> %s by %s: allowed=%v, want %v", from, to, a, got, want)
				}
			}
		}
	}
}

func TestTerminalStatesLeaveNowhere(t *testing.T) {
	for _, from := range []State{Done, Cancelled} {
		for _, to := range allStates {
			for _, a := range allActors {
				if Allowed(from, to, a) {
					t.Errorf("%s -> %s by %s must be illegal", from, to, a)
				}
			}
		}
	}
}

func TestTransitionWritesAuditRowAndRejectsIllegal(t *testing.T) {
	e := newEnv(t)
	job := e.job(t, "v1")

	// Illegal: the job stays put and no audit row is written.
	err := e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := Transition(ctx, q, e.clk.Now(), job.ID, Done, ActorUser, "")
		return err
	})
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("awaiting_links -> done: %v", err)
	}
	// needs_human without a reason is a validation error, not a transition.
	err = e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := Transition(ctx, q, e.clk.Now(), job.ID, NeedsHuman, ActorTracker, "")
		return err
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("needs_human without reason: %v", err)
	}
	if got := e.state(t, job.ID); got != AwaitingLinks {
		t.Fatalf("state after rejected moves = %s", got)
	}

	err = e.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := Transition(ctx, q, e.clk.Now(), job.ID, NeedsHuman, ActorTracker, "because")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.store.Queries().ListDownloadTransitions(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].FromState.Valid || rows[0].ToState != "awaiting_links" ||
		rows[1].FromState.String != "awaiting_links" || rows[1].ToState != "needs_human" || rows[1].Actor != "tracker" || rows[1].Detail.String != "because" {
		t.Fatalf("transition audit: %+v", rows)
	}
	j, _ := e.svc.Get(ctx, job.ID)
	if j.NeedsHumanReason != "because" {
		t.Fatalf("reason = %q", j.NeedsHumanReason)
	}
}
