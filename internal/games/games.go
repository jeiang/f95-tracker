// Package games is the Game aggregate service: Games, their Sources, Play
// status, rating, Play log, list/detail queries, the alert set and settings,
// and the cover cache.
package games

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

type (
	Game    = sqlcgen.Game
	Source  = sqlcgen.Source
	PlayLog = sqlcgen.PlayLog
)

// Options configures a Service. StateDir holds covers/; HTTPClient fetches covers.
type Options struct {
	StateDir   string
	HTTPClient *http.Client
}

type Service struct {
	store  *db.Store
	clock  clock.Clock
	http   *http.Client
	state  string
	covers string
}

func New(store *db.Store, clk clock.Clock, opt Options) *Service {
	hc := opt.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Service{store: store, clock: clk, http: hc, state: opt.StateDir, covers: filepath.Join(opt.StateDir, "covers")}
}

func (s *Service) now() string { return clock.Timestamp(s.clock.Now()) }

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrValidation, fmt.Sprintf(format, a...))
}

func notFound(what string, id int64) error {
	return fmt.Errorf("%w: %s %d", domain.ErrNotFound, what, id)
}

// mapNoRows turns sql.ErrNoRows into ErrNotFound.
func mapNoRows(err error, what string, id int64) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(what, id)
	}
	return err
}

// ConflictError reports a Source that is already tracked; it matches domain.ErrConflict.
type ConflictError struct{ GameID int64 }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: source already tracked by game %d", domain.ErrConflict, e.GameID)
}
func (e *ConflictError) Unwrap() error { return domain.ErrConflict }

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullPtr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// SourceSpec identifies a Source; ExternalID is the F95 thread id or itch.io slug (empty for manual).
type SourceSpec struct {
	Kind       domain.SourceKind
	ExternalID string
	URL        string
}

func (sp SourceSpec) validate() error {
	if !sp.Kind.Valid() {
		return invalid("source kind %q", sp.Kind)
	}
	if sp.Kind == domain.SourceManual {
		if sp.ExternalID != "" {
			return invalid("manual source has no external id")
		}
	} else if strings.TrimSpace(sp.ExternalID) == "" {
		return invalid("%s source needs an external id", sp.Kind)
	}
	u, err := url.Parse(sp.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("source url %q", sp.URL)
	}
	return nil
}

// checkUntracked returns a ConflictError if the Source already belongs to a Game:
// by (kind, external_id), and for non-F95 kinds also by (kind, url).
func checkUntracked(ctx context.Context, q *sqlcgen.Queries, sp SourceSpec) error {
	if sp.ExternalID != "" {
		src, err := q.FindSourceByExternal(ctx, sqlcgen.FindSourceByExternalParams{Kind: string(sp.Kind), ExternalID: nullStr(sp.ExternalID)})
		if err == nil {
			return &ConflictError{GameID: src.GameID}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if sp.Kind != domain.SourceF95Thread {
		src, err := q.FindSourceByURL(ctx, sqlcgen.FindSourceByURLParams{Kind: string(sp.Kind), Url: sp.URL})
		if err == nil {
			return &ConflictError{GameID: src.GameID}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
