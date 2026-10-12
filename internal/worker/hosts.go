package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Host downloads one resolved mirror URL into dir (created by the caller).
// progress may be nil. Implementations resume where they can.
type Host interface {
	Download(ctx context.Context, rawURL, dir string, progress func(done, total int64)) error
}

// errJobCancelled is the context cause when the tracker cancelled the job
// (as opposed to the worker shutting down).
var errJobCancelled = errors.New("job cancelled")

// DefaultHosts wires the three supported hosts. stateDir holds MEGAcmd's and
// gallery-dl's HOME; pixeldrainURL is the API base (injectable for tests).
func DefaultHosts(stateDir, pixeldrainURL string) map[string]Host {
	return map[string]Host{
		"pixeldrain": &Pixeldrain{BaseURL: pixeldrainURL, RetryDelay: 5 * time.Second},
		"mega":       &execHost{tool: "mega-get", home: filepath.Join(stateDir, "mega-home"), args: func(u, d string) []string { return []string{u, d} }, onCancel: megaCancel},
		"gofile":     &execHost{tool: "gallery-dl", home: filepath.Join(stateDir, "gallery-dl-home"), args: func(u, d string) []string { return []string{"-D", d, u} }},
	}
}

// ---- pixeldrain ----

type Pixeldrain struct {
	BaseURL    string
	RetryDelay time.Duration
	HTTP       *http.Client
}

const pixeldrainAttempts = 3

func pixeldrainID(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bad pixeldrain url: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "u" && parts[1] != "":
		return parts[1], nil
	case len(parts) == 3 && parts[0] == "api" && parts[1] == "file":
		return parts[2], nil
	case len(parts) >= 1 && parts[0] == "l":
		return "", errors.New("pixeldrain list links are not supported")
	}
	return "", errors.New("unrecognised pixeldrain url")
}

func (p *Pixeldrain) Download(ctx context.Context, rawURL, dir string, progress func(done, total int64)) error {
	id, err := pixeldrainID(rawURL)
	if err != nil {
		return err
	}
	var info struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := getJSON(ctx, p.client(), p.BaseURL+"/api/file/"+url.PathEscape(id)+"/info", &info); err != nil {
		return fmt.Errorf("pixeldrain info: %w", err)
	}
	name := path.Base(strings.ReplaceAll(info.Name, "\\", "/"))
	if name == "." || name == "/" || name == ".." || name == "" {
		name = id
	}
	part := dir + ".part"
	for attempt := 1; ; attempt++ {
		err = p.fetch(ctx, id, part, info.Size, progress)
		if err == nil || ctx.Err() != nil || attempt == pixeldrainAttempts || errors.Is(err, errPermanent) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(p.RetryDelay):
		}
	}
	if err != nil {
		return err
	}
	st, err := os.Stat(part)
	if err != nil {
		return err
	}
	if info.Size > 0 && st.Size() != info.Size {
		os.Remove(part)
		return fmt.Errorf("pixeldrain size mismatch: got %d, want %d", st.Size(), info.Size)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Rename(part, filepath.Join(dir, name))
}

var errPermanent = errors.New("permanent")

// fetch appends to part, resuming with a Range request when part exists.
func (p *Pixeldrain) fetch(ctx context.Context, id, part string, size int64, progress func(done, total int64)) error {
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
	}
	if size > 0 && have > size {
		os.Remove(part)
		have = 0
	}
	if size > 0 && have == size {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/api/file/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC // server ignored Range
		have = 0
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return errors.New("pixeldrain: stale partial file discarded")
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("pixeldrain: %s: %w", resp.Status, errPermanent)
	default:
		return fmt.Errorf("pixeldrain: %s", resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			have += int64(n)
			if progress != nil {
				progress(have, size)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

func (p *Pixeldrain) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

func getJSON(ctx context.Context, c *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- external-tool hosts (mega-get, gallery-dl) ----

type execHost struct {
	tool     string
	home     string
	args     func(url, dir string) []string
	onCancel func(home string)
}

func (h *execHost) Download(ctx context.Context, rawURL, dir string, progress func(done, total int64)) error {
	// MEGAcmd may re-split its arguments, so refuse anything that could smuggle one.
	if strings.ContainsFunc(rawURL, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("\\\"'`", r)
	}) {
		return errors.New("url contains whitespace, quotes, backslashes or control characters")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(h.home, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, h.tool, h.args(rawURL, dir)...)
	cmd.Env = append(os.Environ(), "HOME="+h.home)
	cmd.WaitDelay = 5 * time.Second

	stop := make(chan struct{})
	if progress != nil {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					progress(dirSize(dir), 0)
				}
			}
		}()
	}
	out, err := cmd.CombinedOutput()
	close(stop)
	if err != nil {
		if ctx.Err() != nil {
			if errors.Is(context.Cause(ctx), errJobCancelled) && h.onCancel != nil {
				h.onCancel(h.home)
			}
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w: %s", h.tool, err, tail(out))
	}
	return nil
}

// megaCancel stops the transfers MEGAcmd's server keeps running after the
// mega-get client is killed.
func megaCancel(home string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mega-transfers", "-c", "-a")
	cmd.Env = append(os.Environ(), "HOME="+home)
	_ = cmd.Run()
}

func dirSize(dir string) int64 {
	files, _ := listFiles(dir)
	var n int64
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			n += st.Size()
		}
	}
	return n
}
