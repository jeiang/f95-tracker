package f95

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://f95zone.to"
	requestTimeout = 30 * time.Second
	maxBody        = 5 << 20
	maxBatch       = 100
)

// DefaultRetry is the check-mode ladder (R-F95-6); interactive callers pass nil.
var DefaultRetry = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// Options configures a Client. Zero values pick production defaults except
// Pacer and Creds, where nil means "no pacing" and "guest only".
type Options struct {
	BaseURL     string                                     // default https://f95zone.to
	HTTP        *http.Client                               // default &http.Client{}; its redirect policy is replaced
	Pacer       *Pacer                                     // cross-process lock; shared by all Clients of the state dir
	Retry       []time.Duration                            // waits before retry 1..n on a block signal; nil = fail once
	Sleep       func(context.Context, time.Duration) error // default: real timer
	Creds       *CredStore
	Version     string // build version for the fallback User-Agent
	Interactive bool   // lock wait capped at Pacer.MaxWait, then ErrBusy
}

// Client talks to F95zone. It is safe for concurrent use.
type Client struct {
	base        *url.URL
	http        *http.Client
	pacer       *Pacer
	retry       []time.Duration
	sleep       func(context.Context, time.Duration) error
	creds       *CredStore
	fallbackUA  string
	interactive bool
}

func New(o Options) (*Client, error) {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	base, err := url.Parse(strings.TrimRight(o.BaseURL, "/"))
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("f95: bad base URL %q", o.BaseURL)
	}
	hc := http.Client{}
	if o.HTTP != nil {
		hc = *o.HTTP
	}
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("f95: too many redirects")
		}
		if req.URL.Host != base.Host {
			return fmt.Errorf("f95: redirect to foreign host %q refused", req.URL.Host)
		}
		return nil
	}
	if o.Sleep == nil {
		o.Sleep = sleepReal
	}
	ver := o.Version
	if ver == "" {
		ver = "dev"
	}
	return &Client{
		base: base, http: &hc, pacer: o.Pacer, retry: o.Retry, sleep: o.Sleep, creds: o.Creds,
		fallbackUA:  "f95-tracker/" + ver + " (+https://github.com/jeiang/f95-tracker)",
		interactive: o.Interactive,
	}, nil
}

func sleepReal(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Close waits for pending pacing holds, so a short-lived process does not leave
// the next one inside the 5 s window.
func (c *Client) Close() {
	if c.pacer != nil {
		c.pacer.Wait()
	}
}

type page struct {
	status int
	url    string
	body   []byte
}

// get performs one paced GET. With sendCookie the stored jar goes out and rotated
// cookies are persisted before the page is returned; the cookie is only ever sent
// to the configured base host.
func (c *Client) get(ctx context.Context, path string, sendCookie bool) (*page, bool, error) {
	var cred Credential
	var haveCred bool
	if c.creds != nil {
		var err error
		if cred, haveCred, err = c.creds.Load(ctx); err != nil {
			return nil, false, err
		}
	}
	ua := c.fallbackUA
	if haveCred && cred.UserAgent != "" {
		ua = cred.UserAgent
	}
	withCookie := sendCookie && haveCred

	if c.pacer != nil {
		lock, err := c.pacer.acquire(ctx, c.interactive)
		if err != nil {
			return nil, withCookie, err
		}
		started := time.Now()
		defer c.pacer.release(lock, started)
	}

	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, c.base.String()+path, nil)
	if err != nil {
		return nil, withCookie, err
	}
	req.Header.Set("User-Agent", ua)
	if withCookie {
		req.Header.Set("Cookie", cred.Jar.Header())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, withCookie, fmt.Errorf("f95: request: %w", scrubURLError(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, withCookie, fmt.Errorf("f95: read body: %w", err)
	}
	if len(body) > maxBody {
		return nil, withCookie, fmt.Errorf("%w: response over %d bytes", ErrParse, maxBody)
	}
	if withCookie {
		rotated := Jar{}
		for _, ck := range resp.Cookies() {
			if keptCookie(ck.Name) && ck.Value != "" && ck.MaxAge >= 0 {
				rotated[ck.Name] = ck.Value
			}
		}
		if len(rotated) > 0 {
			if err := c.creds.MergeRotated(ctx, rotated); err != nil {
				return nil, withCookie, err
			}
		}
	}
	return &page{status: resp.StatusCode, url: resp.Request.URL.String(), body: body}, withCookie, nil
}

// scrubURLError drops the URL from *url.Error so request details never reach logs.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// withRetry runs op, sleeping out the ladder while it reports ErrBlocked.
func (c *Client) withRetry(ctx context.Context, op func() error) error {
	for i := 0; ; i++ {
		err := op()
		if !errors.Is(err, ErrBlocked) || i >= len(c.retry) {
			return err
		}
		if err := c.sleep(ctx, c.retry[i]); err != nil {
			return err
		}
	}
}

// CheckVersions asks checker.php for the latest version string of each thread,
// in batches of at most 100 ids, with no cookie. Ids F95 does not answer are
// absent from the result (a miss, R-F95-11).
func (c *Client) CheckVersions(ctx context.Context, threadIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(threadIDs))
	for start := 0; start < len(threadIDs); start += maxBatch {
		batch := threadIDs[start:min(start+maxBatch, len(threadIDs))]
		var msg map[string]string
		err := c.withRetry(ctx, func() (err error) {
			msg, err = c.checkBatch(ctx, batch)
			return err
		})
		if err != nil {
			return nil, err
		}
		for _, id := range batch {
			if v, ok := msg[id]; ok {
				out[id] = v
			}
		}
	}
	return out, nil
}

func (c *Client) checkBatch(ctx context.Context, ids []string) (map[string]string, error) {
	pg, _, err := c.get(ctx, "/sam/checker.php?threads="+url.QueryEscape(strings.Join(ids, ",")), false)
	if err != nil {
		return nil, err
	}
	if pg.status == http.StatusTooManyRequests || pg.status >= 500 {
		return nil, fmt.Errorf("%w: checker.php status %d", ErrBlocked, pg.status)
	}
	if pg.status != http.StatusOK {
		return nil, fmt.Errorf("%w: checker.php status %d", ErrParse, pg.status)
	}
	var env struct {
		Status string          `json:"status"`
		Msg    json.RawMessage `json:"msg"`
	}
	if err := json.Unmarshal(pg.body, &env); err != nil {
		if looksBlocked(pg.body) {
			return nil, fmt.Errorf("%w: checker.php challenge page", ErrBlocked)
		}
		return nil, fmt.Errorf("%w: checker.php is not JSON", ErrParse)
	}
	if env.Status != "ok" {
		if bytes.Contains(bytes.ToLower(env.Msg), []byte("temporarily blocked")) {
			return nil, fmt.Errorf("%w: checker.php temporarily blocked", ErrBlocked)
		}
		return nil, fmt.Errorf("%w: checker.php status %q", ErrParse, env.Status)
	}
	var msg map[string]string
	if err := json.Unmarshal(env.Msg, &msg); err != nil || len(msg) == 0 {
		return nil, fmt.Errorf("%w: checker.php answered no versions for %d ids", ErrParse, len(ids))
	}
	return msg, nil
}

var (
	loggedInRe = regexp.MustCompile(`<html[^>]*\sdata-logged-in="true"`)
	xenforoRe  = regexp.MustCompile(`<html[^>]*\sdata-logged-in=`)
	blockMarks = []string{"429 too many requests", "error 429", "ddos-guard", "just a moment", "_cf_chl_opt", "maintenance", "temporarily blocked"}
)

func looksBlocked(body []byte) bool {
	head := bytes.ToLower(body[:min(len(body), 64<<10)])
	for _, m := range blockMarks {
		if bytes.Contains(head, []byte(m)) {
			return true
		}
	}
	return false
}

// classify turns a cookie-bearing page response into an error, or nil when the
// body is a real XenForo page (logged in or not, reported as loggedIn).
func (c *Client) classify(ctx context.Context, pg *page, withCookie bool) (loggedIn bool, err error) {
	if pg.status == http.StatusTooManyRequests || pg.status >= 500 {
		return false, fmt.Errorf("%w: status %d", ErrBlocked, pg.status)
	}
	if !xenforoRe.Match(pg.body) {
		if looksBlocked(pg.body) {
			return false, fmt.Errorf("%w: challenge or maintenance page (status %d)", ErrBlocked, pg.status)
		}
		return false, fmt.Errorf("%w: status %d, not a forum page", ErrParse, pg.status)
	}
	loggedIn = loggedInRe.Match(pg.body)
	if withCookie && !loggedIn {
		if _, err := c.creds.MarkInvalid(ctx); err != nil {
			return false, err
		}
		return false, ErrCookieInvalid
	}
	return loggedIn, nil
}

// ThreadHTML fetches the thread page (following redirects) and returns its body
// once classified; the fixture recorder uses it, FetchThread parses it.
func (c *Client) ThreadHTML(ctx context.Context, threadID string) (body []byte, finalURL string, err error) {
	pg, _, err := c.threadPage(ctx, threadID)
	if err != nil {
		return nil, "", err
	}
	return pg.body, pg.url, nil
}

func (c *Client) threadPage(ctx context.Context, threadID string) (pg *page, loggedIn bool, err error) {
	if !validThreadID(threadID) {
		return nil, false, fmt.Errorf("f95: bad thread id %q", threadID)
	}
	err = c.withRetry(ctx, func() error {
		var withCookie bool
		pg, withCookie, err = c.get(ctx, "/threads/"+threadID+"/", true)
		if err != nil {
			return err
		}
		if loggedIn, err = c.classify(ctx, pg, withCookie); err != nil {
			return err
		}
		switch pg.status {
		case http.StatusOK:
		case http.StatusForbidden, http.StatusNotFound:
			return fmt.Errorf("%w: status %d", ErrRestricted, pg.status)
		default:
			return fmt.Errorf("%w: status %d", ErrParse, pg.status)
		}
		if withCookie {
			return c.creds.MarkValid(ctx)
		}
		return nil
	})
	return pg, loggedIn, err
}

func validThreadID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// FetchThread loads and parses a thread page (R-F95-8). Without a stored cookie
// it reads as a guest (HasGenre false).
//
// When the stored cookie is rejected and F95 still serves a readable thread page
// as a guest, it returns that guest parse (LoggedIn false) together with
// ErrCookieInvalid, so Add Game can keep name/version/cover (R-F95-10). Callers
// that must not apply guest data treat any non-nil error as failure. A login or
// 403 page with no thread content gives (nil, ErrCookieInvalid).
func (c *Client) FetchThread(ctx context.Context, threadID string) (*Thread, error) {
	pg, loggedIn, err := c.threadPage(ctx, threadID)
	if errors.Is(err, ErrCookieInvalid) {
		if pg != nil && pg.status == http.StatusOK {
			if th, perr := ParseThread(pg.body, threadID, pg.url, false); perr == nil {
				return th, err
			}
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return ParseThread(pg.body, threadID, pg.url, loggedIn)
}

// Probe loads thread threadID with the stored cookie (R-F95-1 allows no other
// page) and reports whether F95 sees a logged-in user. A stored cookie that gets
// the logged-out page is marked invalid and reported as false, not as an error.
// With no stored cookie it returns false without a request.
func (c *Client) Probe(ctx context.Context, threadID string) (bool, error) {
	if !validThreadID(threadID) {
		return false, fmt.Errorf("f95: bad thread id %q", threadID)
	}
	if c.creds == nil {
		return false, nil
	}
	if _, have, err := c.creds.Load(ctx); err != nil || !have {
		return false, err
	}
	var ok bool
	err := c.withRetry(ctx, func() error {
		pg, _, err := c.get(ctx, "/threads/"+threadID+"/", true)
		if err != nil {
			return err
		}
		if _, err := c.classify(ctx, pg, true); err != nil {
			return err
		}
		ok = true
		return c.creds.MarkValid(ctx)
	})
	if errors.Is(err, ErrCookieInvalid) {
		return false, nil
	}
	return ok, err
}

// SaveAndValidate stores a pasted Cookie header with the pasting request's UA,
// then runs one validation load of threadID (R-SET-1, see CredStore.ProbeThread). tfaExpires is optional (zero = none).
func (c *Client) SaveAndValidate(ctx context.Context, raw, ua string, tfaExpires time.Time, threadID string) (bool, error) {
	jar, err := ParseCookieHeader(raw)
	if err != nil {
		return false, err
	}
	if err := c.creds.Replace(ctx, jar, ua, tfaExpires); err != nil {
		return false, err
	}
	return c.Probe(ctx, threadID)
}
