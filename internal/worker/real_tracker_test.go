package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/api"
	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/testutil"
	wapi "github.com/jeiang/f95-tracker/internal/worker/api"
)

// A job left in extracting by a stopped worker must still finish against the
// real state machine, which has no extracting -> downloading edge.
func TestResumesExtractingJobAgainstRealTracker(t *testing.T) {
	for name, markerFirst := range map[string]bool{"interrupted extraction": false, "done report lost": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := testutil.NewStore(t)
			clk := clock.NewFake(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
			ntfy := testutil.NewNtfyFake(t)
			svc := downloads.New(store, clk, notify.New(store, clk, notify.Options{URL: ntfy.URL, Topic: "t"}))
			tokens := auth.NewTokens(store, clk)
			plain, _, err := tokens.Create(ctx, "worker", auth.ScopeDownloader)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(api.New(svc, tokens, slog.New(slog.NewTextHandler(io.Discard, nil))))
			t.Cleanup(srv.Close)

			testutil.InsertGame(t, store, testutil.GameSpec{Name: "Alpha", ExternalID: "1001", LatestVersion: "v2", PlayStatus: domain.PlayPlaying})
			if _, err := svc.SubmitLinks(ctx, "1001", []downloads.LinkInput{{URL: "https://pixeldrain.com/u/abc"}}); err != nil {
				t.Fatal(err)
			}
			jobs, _, err := svc.WorkerJobs(ctx)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("jobs %v %v", jobs, err)
			}
			job := jobs[0]
			mid := job.Mirrors[0].ID
			for _, ev := range []downloads.Event{
				{State: downloads.Downloading, ActiveMirrorID: &mid, MirrorID: &mid, MirrorState: "active"},
				{State: downloads.Extracting},
			} {
				if _, err := svc.ApplyEvent(ctx, job.ID, ev); err != nil {
					t.Fatal(err)
				}
			}

			games, state := t.TempDir(), t.TempDir()
			bin := t.TempDir()
			script(t, bin, "7zz", `for a; do case "$a" in -o*) d="${a#-o}";; esac; done
mkdir -p "$d" && echo x > "$d/game.exe"`)
			t.Setenv("PATH", bin+":/bin:/usr/bin")
			pd := httptest.NewServer(&fakePixeldrain{files: map[string]fakeFile{"abc": {"g.zip", bytes.Repeat([]byte("z"), 100)}}})
			t.Cleanup(pd.Close)
			w, err := New(Options{
				Client:   &wapi.Client{BaseURL: srv.URL, Token: plain},
				GamesDir: games, PollInterval: time.Second, Hosts: DefaultHosts(state, pd.URL),
			})
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(games, "Alpha", "v2")
			if markerFirst {
				os.MkdirAll(dest, 0o755)
				os.WriteFile(w.markerPath(job.ID), []byte(dest), 0o644)
			}
			if err := w.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Get(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != downloads.Done || got.ResultPath != dest {
				t.Fatalf("job state=%s result=%q", got.State, got.ResultPath)
			}
			if !markerFirst {
				if _, err := os.Stat(filepath.Join(dest, "game.exe")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
