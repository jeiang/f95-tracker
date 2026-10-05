package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
)

// sessionStore is the scs Store over the sessions table. Expiry is stored as
// unix seconds.
type sessionStore struct{ db *db.Store }

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func (s sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	data, err := s.db.Queries().FindSession(ctx, sqlcgen.FindSessionParams{Token: token, Expiry: unix(time.Now())})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (s sessionStore) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	return s.db.WithTx(ctx, func(q *sqlcgen.Queries) error {
		return q.UpsertSession(ctx, sqlcgen.UpsertSessionParams{Token: token, Data: b, Expiry: unix(expiry)})
	})
}

func (s sessionStore) DeleteCtx(ctx context.Context, token string) error {
	return s.db.WithTx(ctx, func(q *sqlcgen.Queries) error { return q.DeleteSession(ctx, token) })
}

func (s sessionStore) deleteExpired(ctx context.Context) error {
	return s.db.WithTx(ctx, func(q *sqlcgen.Queries) error { return q.DeleteExpiredSessions(ctx, unix(time.Now())) })
}

// scs.Store requires the context-free methods too; the manager only calls the
// Ctx variants because the store implements scs.CtxStore.
func (s sessionStore) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}
func (s sessionStore) Commit(token string, b []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, b, expiry)
}
func (s sessionStore) Delete(token string) error { return s.DeleteCtx(context.Background(), token) }
