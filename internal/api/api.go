// Package api is the bearer-token JSON API of the downloader worker and the links userscript
// (spec 4.1). There is no CORS: the userscript uses GM_xmlhttpRequest.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/downloads"
)

const maxBody = 1 << 20

type handler struct {
	dl  *downloads.Service
	log *slog.Logger
}

// New returns the /api/ handler. Every request must carry a valid bearer token whose scope
// covers the path (auth.Tokens.Bearer).
func New(dl *downloads.Service, tokens *auth.Tokens, log *slog.Logger) http.Handler {
	h := &handler{dl: dl, log: log.With("component", "api")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/downloader/jobs", h.workerJobs)
	mux.HandleFunc("POST /api/v1/downloader/jobs/{id}/events", h.jobEvent)
	mux.HandleFunc("GET /api/v1/links/pending", h.linksPending)
	mux.HandleFunc("POST /api/v1/links", h.submitLinks)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	return tokens.Bearer(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// fail maps a service error onto the API's status codes and error codes.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, downloads.ErrMasked):
		writeError(w, http.StatusUnprocessableEntity, "masked_url", "Masked f95zone.to links are not accepted; unmask them first.")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrIllegalTransition):
		writeError(w, http.StatusConflict, "illegal_transition", err.Error())
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	default:
		h.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.Join(domain.ErrValidation, err)
	}
	return nil
}

type mirrorJSON struct {
	ID        int64   `json:"id"`
	Position  int     `json:"position"`
	Host      string  `json:"host"`
	URL       string  `json:"url"`
	Platform  *string `json:"platform"`
	Supported bool    `json:"supported"`
	State     string  `json:"state"`
	Error     string  `json:"error,omitempty"`
}

type jobJSON struct {
	ID                    int64        `json:"id"`
	Game                  gameJSON     `json:"game"`
	TargetVersion         string       `json:"target_version"`
	State                 string       `json:"state"`
	EffectivePlatformPref string       `json:"effective_platform_pref"`
	NeedsHumanReason      string       `json:"needs_human_reason,omitempty"`
	ActiveMirrorID        int64        `json:"active_mirror_id,omitempty"`
	BytesDone             *int64       `json:"bytes_done,omitempty"`
	BytesTotal            *int64       `json:"bytes_total,omitempty"`
	Mirrors               []mirrorJSON `json:"mirrors"`
}

type gameJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func jobView(j downloads.Job) jobJSON {
	out := jobJSON{
		ID: j.ID, Game: gameJSON{j.GameID, j.GameName}, TargetVersion: j.TargetVersion, State: string(j.State),
		EffectivePlatformPref: string(j.EffectivePlatformPref), NeedsHumanReason: j.NeedsHumanReason,
		ActiveMirrorID: j.ActiveMirrorID, BytesDone: j.BytesDone, BytesTotal: j.BytesTotal, Mirrors: []mirrorJSON{},
	}
	for _, m := range j.Mirrors {
		mj := mirrorJSON{ID: m.ID, Position: m.Position, Host: m.Host, URL: m.URL, Supported: m.Supported, State: m.State, Error: m.Error}
		if m.Platform != "" && m.Platform != downloads.PlatformOther {
			p := m.Platform
			mj.Platform = &p
		}
		out.Mirrors = append(out.Mirrors, mj)
	}
	return out
}

func (h *handler) workerJobs(w http.ResponseWriter, r *http.Request) {
	jobs, cancelled, err := h.dl.WorkerJobs(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp := struct {
		Jobs      []jobJSON `json:"jobs"`
		Cancelled []int64   `json:"cancelled"`
	}{Jobs: []jobJSON{}, Cancelled: []int64{}}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, jobView(j))
	}
	resp.Cancelled = append(resp.Cancelled, cancelled...)
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) jobEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such job.")
		return
	}
	var body struct {
		State            string `json:"state"`
		ActiveMirrorID   *int64 `json:"active_mirror_id"`
		MirrorID         *int64 `json:"mirror_id"`
		MirrorState      string `json:"mirror_state"`
		Error            string `json:"error"`
		BytesDone        *int64 `json:"bytes_done"`
		BytesTotal       *int64 `json:"bytes_total"`
		NeedsHumanReason string `json:"needs_human_reason"`
		ResultPath       string `json:"result_path"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	job, err := h.dl.ApplyEvent(r.Context(), id, downloads.Event{
		State: downloads.State(body.State), ActiveMirrorID: body.ActiveMirrorID, MirrorID: body.MirrorID,
		MirrorState: body.MirrorState, Error: body.Error, BytesDone: body.BytesDone, BytesTotal: body.BytesTotal,
		NeedsHumanReason: body.NeedsHumanReason, ResultPath: body.ResultPath,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": jobView(job)})
}

func (h *handler) linksPending(w http.ResponseWriter, r *http.Request) {
	thread := r.URL.Query().Get("thread")
	if n, err := strconv.ParseInt(thread, 10, 64); err != nil || n <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "thread must be an F95 thread id.")
		return
	}
	job, err := h.dl.PendingForThread(r.Context(), thread)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "game_name": job.GameName, "target_version": job.TargetVersion})
}

func (h *handler) submitLinks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ThreadID int64 `json:"thread_id"`
		Links    []struct {
			URL      string `json:"url"`
			Platform string `json:"platform"`
			Label    string `json:"label"`
		} `json:"links"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if body.ThreadID <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "thread_id is required.")
		return
	}
	in := make([]downloads.LinkInput, len(body.Links))
	for i, l := range body.Links {
		in[i] = downloads.LinkInput{URL: l.URL, Platform: l.Platform, Label: l.Label}
	}
	res, err := h.dl.SubmitLinks(r.Context(), strconv.FormatInt(body.ThreadID, 10), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": res.Job.ID, "state": res.Job.State, "accepted": res.Accepted, "manual": res.Manual})
}
