package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/domain"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

func newTokens(t *testing.T) (*Tokens, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	return NewTokens(testutil.NewStore(t), clk), clk
}

func TestTokenCreateVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	tk, _ := newTokens(t)
	plain, tok, err := tk.Create(ctx, " artemis ", ScopeDownloader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "f95t_") || len(plain) != 5+43 || tok.Name != "artemis" || tok.Scope != ScopeDownloader {
		t.Fatalf("token %q %+v", plain, tok)
	}
	other, _, _ := tk.Create(ctx, "second", ScopeSubmitLinks)
	if other == plain {
		t.Fatal("tokens must be random")
	}

	got, err := tk.Verify(ctx, plain)
	if err != nil || got.ID != tok.ID || got.Scope != ScopeDownloader {
		t.Fatalf("verify: %+v %v", got, err)
	}
	// The plaintext is stored nowhere: only its hash is.
	rows, _ := tk.List(ctx)
	if len(rows) != 2 {
		t.Fatalf("list: %+v", rows)
	}
	sum := sha256.Sum256([]byte(plain))
	row, err := tk.store.Queries().GetAPITokenByHash(ctx, sum[:])
	if err != nil || row.ID != tok.ID || len(row.TokenHash) != 32 {
		t.Fatalf("token not stored as its SHA-256: %+v %v", row, err)
	}

	for _, bad := range []string{"", "f95t_", plain[:len(plain)-1], plain + "x", "f95t_" + strings.Repeat("A", 43), "Bearer " + plain, strings.TrimPrefix(plain, "f95t_")} {
		if _, err := tk.Verify(ctx, bad); !errors.Is(err, ErrBadToken) {
			t.Errorf("Verify(%q): %v", bad, err)
		}
	}

	if err := tk.Revoke(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Verify(ctx, plain); !errors.Is(err, ErrBadToken) {
		t.Fatalf("revoked token verified: %v", err)
	}
	if _, err := tk.Verify(ctx, other); err != nil {
		t.Fatalf("revoking one token touched another: %v", err)
	}
	if rows, _ := tk.List(ctx); len(rows) != 1 || rows[0].Name != "second" {
		t.Fatalf("list after revoke: %+v", rows)
	}
	if err := tk.Revoke(ctx, tok.ID); err != nil {
		t.Fatalf("revoking twice: %v", err)
	}
}

func TestTokenCreateValidation(t *testing.T) {
	tk, _ := newTokens(t)
	for _, c := range []struct {
		name  string
		scope Scope
	}{{"", ScopeDownloader}, {"  ", ScopeDownloader}, {"x", "admin"}, {"x", ""}} {
		if _, _, err := tk.Create(context.Background(), c.name, c.scope); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("Create(%q, %q): %v", c.name, c.scope, err)
		}
	}
}

func TestLastUsedIsStampedAtMostOncePerMinute(t *testing.T) {
	ctx := context.Background()
	tk, clk := newTokens(t)
	plain, tok, _ := tk.Create(ctx, "w", ScopeDownloader)
	lastUsed := func() string {
		rows, _ := tk.List(ctx)
		for _, r := range rows {
			if r.ID == tok.ID {
				return r.LastUsedAt
			}
		}
		return "?"
	}
	if lastUsed() != "" {
		t.Fatalf("never used yet: %q", lastUsed())
	}
	steps := []struct {
		advance time.Duration
		want    string
	}{
		{0, "2026-03-01T08:00:00Z"},
		{59 * time.Second, "2026-03-01T08:00:00Z"}, // within the minute: untouched
		{time.Second, "2026-03-01T08:01:00Z"},      // a full minute later: stamped again
		{30 * time.Second, "2026-03-01T08:01:00Z"},
	}
	for i, s := range steps {
		clk.Advance(s.advance)
		if _, err := tk.Verify(ctx, plain); err != nil {
			t.Fatal(err)
		}
		if got := lastUsed(); got != s.want {
			t.Errorf("step %d: last_used_at = %s, want %s", i, got, s.want)
		}
	}
}

func TestBearerMiddlewareStatusAndScope(t *testing.T) {
	ctx := context.Background()
	tk, _ := newTokens(t)
	dl, _, _ := tk.Create(ctx, "worker", ScopeDownloader)
	sub, subTok, _ := tk.Create(ctx, "userscript", ScopeSubmitLinks)
	revoked, rt, _ := tk.Create(ctx, "old", ScopeDownloader)
	_ = tk.Revoke(ctx, rt.ID)

	var seen Token
	h := tk.Bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = TokenFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	cases := []struct {
		name, path, header string
		status             int
		code               string
	}{
		{"no header", "/api/v1/downloader/jobs", "", 401, "unauthorized"},
		{"not bearer", "/api/v1/downloader/jobs", "Basic " + dl, 401, "unauthorized"},
		{"garbage", "/api/v1/downloader/jobs", "Bearer nope", 401, "unauthorized"},
		{"revoked", "/api/v1/downloader/jobs", "Bearer " + revoked, 401, "unauthorized"},
		{"downloader ok", "/api/v1/downloader/jobs/3/events", "Bearer " + dl, 204, ""},
		{"downloader on links", "/api/v1/links/pending", "Bearer " + dl, 403, "forbidden"},
		{"downloader on links post", "/api/v1/links", "Bearer " + dl, 403, "forbidden"},
		{"submit ok pending", "/api/v1/links/pending", "Bearer " + sub, 204, ""},
		{"submit ok post", "/api/v1/links", "Bearer " + sub, 204, ""},
		{"submit on downloader", "/api/v1/downloader/jobs", "Bearer " + sub, 403, "forbidden"},
		{"submit on other api", "/api/v1/games", "Bearer " + sub, 403, "forbidden"},
		{"downloader on other api", "/api/v1/games", "Bearer " + dl, 403, "forbidden"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.status)
		}
		if c.code != "" && (!strings.Contains(rec.Body.String(), `"error":"`+c.code+`"`) || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json")) {
			t.Errorf("%s: body %q %v", c.name, rec.Body.String(), rec.Header())
		}
	}
	if seen.ID != subTok.ID {
		t.Errorf("handler saw token %+v", seen)
	}
}
