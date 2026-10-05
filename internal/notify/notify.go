// Package notify delivers ntfy pushes: the daily digest and the immediate
// alerts (R-NOTIF). Every attempt is a notification row.
package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
)

// Options configure delivery. URL and Topic empty = notifications disabled.
type Options struct {
	URL, Topic string
	Token      config.Secret
	BaseURL    string // the tracker's own base URL, used for Click links
	HTTPClient *http.Client
}

type Notifier struct {
	store *db.Store
	clock clock.Clock
	opts  Options
}

func New(store *db.Store, clk clock.Clock, opts Options) *Notifier {
	return &Notifier{store: store, clock: clk, opts: opts}
}

// Enabled reports whether an ntfy URL and topic are configured.
func (n *Notifier) Enabled() bool { return n.opts.URL != "" && n.opts.Topic != "" }

func (n *Notifier) client() *http.Client {
	if n.opts.HTTPClient != nil {
		return n.opts.HTTPClient
	}
	return http.DefaultClient
}

// SendDigest sends the daily digest when the input has news (R-NOTIF-5) and
// records it with its covered items. A failed digest is recorded, not retried.
func (n *Notifier) SendDigest(ctx context.Context, d Digest) (bool, error) {
	if !n.Enabled() || !d.hasNews() {
		return false, nil
	}
	body, cov := buildBody(d)
	title := "F95 Tracker: daily digest"
	var id int64
	err := n.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.InsertNotification(ctx, sqlcgen.InsertNotificationParams{
			Kind: "digest", RunID: nullID(d.RunID), Title: title, Body: body, CreatedAt: clock.Timestamp(n.clock.Now()),
		})
		if err != nil {
			return err
		}
		id = row.ID
		for _, c := range cov {
			if err := q.InsertNotificationItem(ctx, sqlcgen.InsertNotificationItemParams{
				NotificationID: id, GameID: c.game, CheckResultID: c.result, DownloadJobID: c.job,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	sendErr := n.post(ctx, message{Title: title, Body: body, Priority: 3, Click: strings.TrimRight(n.opts.BaseURL, "/") + "/games?updates=1"})
	return sendErr == nil, errors.Join(n.settle(ctx, id, sendErr), sendErr)
}

// settle records the outcome of an attempt on its notification row and returns
// only a recording failure; callers report the delivery error themselves.
func (n *Notifier) settle(ctx context.Context, id int64, sendErr error) error {
	return n.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		if sendErr != nil {
			return q.MarkNotificationFailed(ctx, sqlcgen.MarkNotificationFailedParams{Error: sql.NullString{String: sendErr.Error(), Valid: true}, ID: id})
		}
		return q.MarkNotificationSent(ctx, sqlcgen.MarkNotificationSentParams{SentAt: sql.NullString{String: clock.Timestamp(n.clock.Now()), Valid: true}, ID: id})
	})
}

// CookieInvalid pushes once per invalidation: the claim on invalid_alerted_at
// and the notification row commit together, so concurrent callers cannot both
// push. It does nothing unless the credential is currently invalid.
func (n *Notifier) CookieInvalid(ctx context.Context) error {
	if !n.Enabled() {
		return nil
	}
	return n.immediate(ctx, "cookie_invalid", "F95 cookie invalid",
		"The F95 cookie was rejected. Paste a fresh one in Settings.",
		func(q *sqlcgen.Queries, now string) (bool, int64, error) {
			rows, err := q.ClaimCookieInvalidAlert(ctx, sql.NullString{String: now, Valid: true})
			return rows == 1, 0, err
		})
}

// SourceUnavailable pushes once per transition to unavailable: it is skipped
// when the Game already has a push at or after the Source's unavailable_at.
func (n *Notifier) SourceUnavailable(ctx context.Context, sourceID int64, gameName string) error {
	if !n.Enabled() {
		return nil
	}
	return n.immediate(ctx, "source_unavailable", "Source unavailable",
		fmt.Sprintf("%s is no longer available at its Source and is excluded from checks.", gameName),
		func(q *sqlcgen.Queries, _ string) (bool, int64, error) {
			info, err := q.GetSourceNotifyInfo(ctx, sourceID)
			if err != nil {
				return false, 0, err
			}
			done, err := q.SourceUnavailableAlerted(ctx, sqlcgen.SourceUnavailableAlertedParams{
				GameID: nullID(info.GameID), CreatedAt: info.UnavailableAt.String,
			})
			return !done, info.GameID, err
		})
}

// immediate runs claim in a tx; when it grants the push, the row (and game
// item) is written in that same tx, then the push is sent and settled.
func (n *Notifier) immediate(ctx context.Context, kind, event, body string, claim func(q *sqlcgen.Queries, now string) (ok bool, gameID int64, err error)) error {
	title := "F95 Tracker: " + event
	var id int64
	granted := false
	err := n.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		now := clock.Timestamp(n.clock.Now())
		ok, gameID, err := claim(q, now)
		if err != nil || !ok {
			return err
		}
		row, err := q.InsertNotification(ctx, sqlcgen.InsertNotificationParams{Kind: kind, Title: title, Body: body, CreatedAt: now})
		if err != nil {
			return err
		}
		id, granted = row.ID, true
		if gameID != 0 {
			return q.InsertNotificationItem(ctx, sqlcgen.InsertNotificationItemParams{NotificationID: id, GameID: nullID(gameID)})
		}
		return nil
	})
	if err != nil || !granted {
		return err
	}
	sendErr := n.post(ctx, message{Title: title, Body: body, Priority: 4})
	return errors.Join(n.settle(ctx, id, sendErr), sendErr)
}

// Test sends a priority-3 test push for Settings and returns the delivery
// error. The notification table has no kind for tests, so none is recorded.
func (n *Notifier) Test(ctx context.Context) error {
	return n.post(ctx, message{Title: "F95 Tracker: test", Body: "Notifications are working.", Priority: 3})
}

// RetryUnsent re-sends unsent immediate pushes (never digests) and returns how
// many were delivered. Call it at the start of each check run.
func (n *Notifier) RetryUnsent(ctx context.Context) (int, error) {
	if !n.Enabled() {
		return 0, nil
	}
	rows, err := n.store.Queries().ListUnsentImmediateNotifications(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, r := range rows {
		sendErr := n.post(ctx, message{Title: r.Title, Body: r.Body, Priority: 4})
		if err := n.settle(ctx, r.ID, sendErr); err != nil {
			errs = append(errs, err)
		} else if sendErr == nil {
			sent++
		}
	}
	return sent, errors.Join(errs...)
}
