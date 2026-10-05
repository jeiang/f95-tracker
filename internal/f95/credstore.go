package f95

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// Credential is the stored jar plus the browser User-Agent of the pasting request.
type Credential struct {
	Jar       Jar
	UserAgent string
}

// CredStore persists the F95 cookie over the f95_credential row.
type CredStore struct {
	store *db.Store
	clock clock.Clock
}

func NewCredStore(store *db.Store, c clock.Clock) *CredStore {
	return &CredStore{store: store, clock: c}
}

// Get returns the raw row (validity, validated_at, invalid_alerted_at, ...);
// sql.ErrNoRows when no cookie was ever saved.
func (s *CredStore) Get(ctx context.Context) (sqlcgen.F95Credential, error) {
	return s.store.Queries().GetF95Credential(ctx)
}

// Load returns the stored credential; ok is false when none was saved.
func (s *CredStore) Load(ctx context.Context) (cred Credential, ok bool, err error) {
	row, err := s.Get(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, false, nil
	}
	if err != nil {
		return Credential{}, false, err
	}
	jar, err := unmarshalJar(row.CookieJar)
	if err != nil {
		return Credential{}, false, err
	}
	return Credential{Jar: jar, UserAgent: row.UserAgent}, true, nil
}

// Replace stores a freshly pasted jar and UA with validity back to unknown.
// tfaExpires is optional (zero = unknown).
func (s *CredStore) Replace(ctx context.Context, jar Jar, ua string, tfaExpires time.Time) error {
	if ua == "" {
		return fmt.Errorf("%w: user agent is required", domain.ErrValidation)
	}
	exp := sql.NullString{}
	if !tfaExpires.IsZero() {
		exp = sql.NullString{String: clock.Date(tfaExpires), Valid: true}
	}
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		return q.ReplaceF95Credential(ctx, sqlcgen.ReplaceF95CredentialParams{
			CookieJar: jar.marshal(), UserAgent: ua, TfaTrustExpiresAt: exp,
			UpdatedAt: clock.Timestamp(s.clock.Now()),
		})
	})
}

// MergeRotated overlays rotated Set-Cookie values onto the stored jar. It reads
// and writes in one transaction so concurrent rotations never lose a value.
func (s *CredStore) MergeRotated(ctx context.Context, rotated Jar) error {
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetF95Credential(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		jar, err := unmarshalJar(row.CookieJar)
		if err != nil {
			return err
		}
		merged := maps.Clone(jar)
		maps.Copy(merged, rotated)
		if maps.Equal(jar, merged) {
			return nil
		}
		return q.UpdateF95CredentialJar(ctx, sqlcgen.UpdateF95CredentialJarParams{
			CookieJar: merged.marshal(), UpdatedAt: clock.Timestamp(s.clock.Now()),
		})
	})
}

// MarkValid records a successful logged-in load and re-arms the one-alert-per-
// transition flag.
func (s *CredStore) MarkValid(ctx context.Context) error {
	now := sql.NullString{String: clock.Timestamp(s.clock.Now()), Valid: true}
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return q.MarkF95CredentialValid(ctx, now) })
}

// MarkInvalid flips validity to 'invalid'; flipped is true only on the transition
// (callers send the one push then and call MarkAlerted).
func (s *CredStore) MarkInvalid(ctx context.Context) (flipped bool, err error) {
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.MarkF95CredentialInvalid(ctx, clock.Timestamp(s.clock.Now()))
		flipped = n > 0
		return err
	})
	return flipped, err
}

// MarkAlerted records that the invalid-cookie push went out.
func (s *CredStore) MarkAlerted(ctx context.Context) error {
	now := sql.NullString{String: clock.Timestamp(s.clock.Now()), Valid: true}
	return s.store.WithTx(ctx, func(q *sqlcgen.Queries) error { return q.MarkF95CredentialAlerted(ctx, now) })
}

// fallbackProbeThread is a long-lived public thread (Out of Touch!, in the CSV
// and readable logged in) used when no tracked Source is available.
const fallbackProbeThread = "67494"

// ProbeThread picks the thread for a cookie probe: the oldest tracked readable
// F95 primary Source, else a fixed public thread.
func (s *CredStore) ProbeThread(ctx context.Context) (string, error) {
	id, err := s.store.Queries().OldestReadableF95Thread(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return fallbackProbeThread, nil
	}
	if err != nil {
		return "", err
	}
	return id.String, nil
}
