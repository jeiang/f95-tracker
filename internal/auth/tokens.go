package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

// Scope is what an API token may call (R-TOK-4).
type Scope string

const (
	ScopeSubmitLinks Scope = "submit_links" // /api/v1/links*
	ScopeDownloader  Scope = "downloader"   // /api/v1/downloader/*
)

func (s Scope) Valid() bool { return s == ScopeSubmitLinks || s == ScopeDownloader }

const (
	tokenPrefix    = "f95t_"
	tokenBytes     = 32
	lastUsedWindow = time.Minute
)

// Prefixes of the API paths each scope may call.
var scopePrefix = map[Scope]string{
	ScopeSubmitLinks: "/api/v1/links",
	ScopeDownloader:  "/api/v1/downloader/",
}

// ErrBadToken: missing, malformed, unknown or revoked token.
var ErrBadToken = errors.New("invalid API token")

// Token is a stored API token; the plaintext is never part of it.
type Token struct {
	ID         int64
	Name       string
	Scope      Scope
	CreatedAt  string
	LastUsedAt string // "" = never
}

// Tokens issues, verifies and revokes API tokens (R-TOK).
type Tokens struct {
	store *db.Store
	clock clock.Clock
}

func NewTokens(store *db.Store, clk clock.Clock) *Tokens { return &Tokens{store: store, clock: clk} }

func hashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

func tokenOf(r sqlcgen.ApiToken) Token {
	return Token{ID: r.ID, Name: r.Name, Scope: Scope(r.Scope), CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt.String}
}

// Create issues a token. The plaintext (f95t_<base64url(32 random bytes)>) is returned only here;
// the database keeps its SHA-256.
func (t *Tokens) Create(ctx context.Context, name string, scope Scope) (plaintext string, tok Token, err error) {
	name = strings.TrimSpace(name)
	if name == "" || !scope.Valid() {
		return "", Token{}, fmt.Errorf("%w: a token needs a name and a scope", domain.ErrValidation)
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, err
	}
	plaintext = tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	err = t.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.InsertAPIToken(ctx, sqlcgen.InsertAPITokenParams{
			Name: name, Scope: string(scope), TokenHash: hashToken(plaintext), CreatedAt: clock.Timestamp(t.clock.Now()),
		})
		tok = tokenOf(row)
		return err
	})
	if err != nil {
		return "", Token{}, err
	}
	return plaintext, tok, nil
}

// List returns the active (not revoked) tokens, oldest first.
func (t *Tokens) List(ctx context.Context) ([]Token, error) {
	rows, err := t.store.Queries().ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Token, len(rows))
	for i, r := range rows {
		out[i] = tokenOf(r)
	}
	return out, nil
}

// Revoke makes a token authenticate nothing; revoking an unknown or revoked token is a no-op.
func (t *Tokens) Revoke(ctx context.Context, id int64) error {
	return t.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := q.RevokeAPIToken(ctx, sqlcgen.RevokeAPITokenParams{RevokedAt: sql.NullString{String: clock.Timestamp(t.clock.Now()), Valid: true}, ID: id})
		return err
	})
}

// Verify returns the token behind a plaintext, or ErrBadToken. It stamps last_used_at at most
// once per minute.
func (t *Tokens) Verify(ctx context.Context, plaintext string) (Token, error) {
	if !strings.HasPrefix(plaintext, tokenPrefix) || len(plaintext) != len(tokenPrefix)+43 {
		return Token{}, ErrBadToken
	}
	row, err := t.store.Queries().GetAPITokenByHash(ctx, hashToken(plaintext))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrBadToken
	}
	if err != nil {
		return Token{}, err
	}
	now := t.clock.Now()
	if last, err := clock.ParseTimestamp(row.LastUsedAt.String); err != nil || now.Sub(last) >= lastUsedWindow {
		err := t.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
			return q.TouchAPIToken(ctx, sqlcgen.TouchAPITokenParams{
				LastUsedAt: sql.NullString{String: clock.Timestamp(now), Valid: true}, ID: row.ID,
				LastUsedAt_2: sql.NullString{String: clock.Timestamp(now.Add(-lastUsedWindow)), Valid: true},
			})
		})
		if err != nil {
			return Token{}, err
		}
		row.LastUsedAt = sql.NullString{String: clock.Timestamp(now), Valid: true}
	}
	return tokenOf(row), nil
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

// Bearer guards the JSON API: it needs `Authorization: Bearer f95t_...`, answering 401
// {"error":"unauthorized"} for a missing, malformed, unknown or revoked token and 403
// {"error":"forbidden"} when the token's scope does not cover the path (R-TOK-4). The token is
// passed on in the request context (see TokenFrom).
func (t *Tokens) Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plain, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			apiError(w, http.StatusUnauthorized, "unauthorized", "A bearer token is required.")
			return
		}
		tok, err := t.Verify(r.Context(), strings.TrimSpace(plain))
		switch {
		case errors.Is(err, ErrBadToken):
			apiError(w, http.StatusUnauthorized, "unauthorized", "The token is invalid or revoked.")
			return
		case err != nil:
			apiError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			return
		}
		if !strings.HasPrefix(r.URL.Path, scopePrefix[tok.Scope]) {
			apiError(w, http.StatusForbidden, "forbidden", "The token's scope does not allow this endpoint.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok)))
	})
}

type tokenKey struct{}

// TokenFrom returns the token Bearer authenticated for the request.
func TokenFrom(ctx context.Context) (Token, bool) {
	tok, ok := ctx.Value(tokenKey{}).(Token)
	return tok, ok
}
