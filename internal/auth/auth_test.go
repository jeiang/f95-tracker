package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/testutil"
)

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"/games?q=a":         "/games?q=a",
		"/":                  "/",
		"":                   "/",
		"games":              "/",
		"//evil.example":     "/",
		`/\evil.example`:     "/",
		"https://evil.test/": "/",
		"/%0d%0a":            "/%0d%0a",
		"/a\nb":              "/",
	}
	for in, want := range cases {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionStoreExpiryAndReplace(t *testing.T) {
	s := sessionStore{db: testutil.NewStore(t)}
	ctx := context.Background()
	if err := s.CommitCtx(ctx, "t1", []byte("a"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCtx(ctx, "t1", []byte("b"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := s.FindCtx(ctx, "t1"); err != nil || !ok || string(b) != "b" {
		t.Fatalf("find: %q %v %v", b, ok, err)
	}
	if err := s.CommitCtx(ctx, "old", []byte("x"), time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FindCtx(ctx, "old"); ok {
		t.Fatal("expired session found")
	}
	if err := s.deleteExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.db.Queries().CountSessions(ctx); n != 1 {
		t.Fatalf("rows after cleanup: %d", n)
	}
	if err := s.DeleteCtx(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FindCtx(ctx, "t1"); ok {
		t.Fatal("deleted session found")
	}
}

// The fake must refuse a code redeemed with the wrong PKCE verifier, otherwise
// the login-flow tests would not prove the client sends the right one.
func TestFakeRejectsWrongPKCEVerifier(t *testing.T) {
	f := testutil.NewOIDCFake(t)
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := url.Values{"response_type": {"code"}, "client_id": {f.ClientID}, "redirect_uri": {"http://f95.test/auth/callback"},
		"state": {"s"}, "nonce": {"n"}, "code_challenge_method": {"S256"},
		"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}} // S256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	resp, err := cl.Get(f.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	u, _ := url.Parse(resp.Header.Get("Location"))
	code := u.Query().Get("code")
	for verifier, want := range map[string]int{"wrong": 400, "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk": 200} {
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://f95.test/auth/callback"}, "code_verifier": {verifier}}
		req, _ := http.NewRequest("POST", f.URL+"/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(f.ClientID, f.ClientSecret)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != want {
			t.Errorf("verifier %q: %d, want %d", verifier, r.StatusCode, want)
		}
		if want == 400 { // the code is consumed by the failed attempt; mint another
			resp, _ := cl.Get(f.URL + "/authorize?" + q.Encode())
			resp.Body.Close()
			u, _ = url.Parse(resp.Header.Get("Location"))
			code = u.Query().Get("code")
		}
	}
}
