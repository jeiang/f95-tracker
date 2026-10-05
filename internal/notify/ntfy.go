package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type message struct {
	Title    string
	Body     string
	Priority int
	Click    string
}

// post delivers one message to ntfy; any non-2xx answer is an error.
func (n *Notifier) post(ctx context.Context, m message) error {
	if !n.Enabled() {
		return fmt.Errorf("ntfy is not configured")
	}
	u := strings.TrimRight(n.opts.URL, "/") + "/" + n.opts.Topic
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(m.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", m.Title)
	req.Header.Set("Priority", fmt.Sprint(m.Priority))
	if m.Click != "" {
		req.Header.Set("Click", m.Click)
	}
	if n.opts.Token.IsSet() {
		req.Header.Set("Authorization", "Bearer "+n.opts.Token.Reveal())
	}
	resp, err := n.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy answered %s", resp.Status)
	}
	return nil
}
