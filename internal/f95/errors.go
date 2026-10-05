// Package f95 is the F95zone HTTP client: cross-process pacing, the cookie jar,
// retries, checker.php, thread-page parsing and the cookie probe (spec R-F95).
package f95

import "errors"

var (
	// ErrCookieInvalid: a logged-in request was answered with a logged-out page.
	ErrCookieInvalid = errors.New("f95: cookie invalid")
	// ErrBlocked: 429, 5xx, challenge, maintenance or "temporarily blocked", after the retry ladder.
	ErrBlocked = errors.New("f95: blocked or rate limited")
	// ErrRestricted: the thread is unreadable (403 permission page, 404).
	ErrRestricted = errors.New("f95: thread restricted or unavailable")
	// ErrParse: a response failed the sanity checks of R-F95-13.
	ErrParse = errors.New("f95: unexpected response")
	// ErrBusy: the cross-process F95 lock was not free within the interactive wait cap.
	ErrBusy = errors.New("f95: busy, try again")
)
