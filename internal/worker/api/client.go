// Package api is the downloader worker's client for the tracker's
// /api/v1/downloader endpoints.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Mirror struct {
	ID        int64   `json:"id"`
	Position  int     `json:"position"`
	Host      string  `json:"host"`
	URL       string  `json:"url"`
	Platform  *string `json:"platform"`
	Supported bool    `json:"supported"`
	State     string  `json:"state"`
}

type Job struct {
	ID   int64 `json:"id"`
	Game struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"game"`
	TargetVersion         string   `json:"target_version"`
	State                 string   `json:"state"`
	EffectivePlatformPref string   `json:"effective_platform_pref"`
	Mirrors               []Mirror `json:"mirrors"`
}

type JobsResponse struct {
	Jobs      []Job   `json:"jobs"`
	Cancelled []int64 `json:"cancelled"`
}

// Event is the body of POST /jobs/{id}/events; every field is optional.
type Event struct {
	State            string `json:"state,omitempty"`
	ActiveMirrorID   *int64 `json:"active_mirror_id,omitempty"`
	MirrorID         *int64 `json:"mirror_id,omitempty"`
	MirrorState      string `json:"mirror_state,omitempty"`
	Error            string `json:"error,omitempty"`
	BytesDone        *int64 `json:"bytes_done,omitempty"`
	BytesTotal       *int64 `json:"bytes_total,omitempty"`
	NeedsHumanReason string `json:"needs_human_reason,omitempty"`
	ResultPath       string `json:"result_path,omitempty"`
}

// Error is a non-2xx tracker response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("tracker %d %s: %s", e.Status, e.Code, e.Message)
}

// IsIllegalTransition reports a 409 illegal_transition response.
func IsIllegalTransition(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusConflict && e.Code == "illegal_transition"
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c *Client) Jobs(ctx context.Context) (JobsResponse, error) {
	var out JobsResponse
	err := c.do(ctx, http.MethodGet, "/api/v1/downloader/jobs", nil, &out)
	return out, err
}

// PostEvent returns the job as the tracker sees it after the event.
func (c *Client) PostEvent(ctx context.Context, jobID int64, ev Event) (Job, error) {
	var out struct {
		Job Job `json:"job"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/downloader/jobs/%d/events", jobID), ev, &out)
	return out.Job, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		var b struct{ Error, Message string }
		if json.Unmarshal(data, &b) == nil {
			e.Code, e.Message = b.Error, b.Message
		}
		return e
	}
	return json.Unmarshal(data, out)
}
