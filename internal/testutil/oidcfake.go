package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/jeiang/f95-tracker/internal/config"
)

// OIDCFake is an httptest OIDC provider: discovery, JWKS, an authorize endpoint
// that immediately redirects back with a code for Sub, and a token endpoint that
// checks the client secret and the S256 PKCE verifier and returns a signed ID
// token carrying the request's nonce.
type OIDCFake struct {
	URL          string // issuer
	ClientID     string
	ClientSecret string

	mu       sync.Mutex
	sub      string
	badNonce bool
	codes    map[string]fakeCode
	key      *ecdsa.PrivateKey
}

type fakeCode struct{ challenge, nonce, redirect string }

func NewOIDCFake(t testing.TB) *OIDCFake {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &OIDCFake{ClientID: "f95-tracker", ClientSecret: "fake-client-secret", sub: "user-1", codes: map[string]fakeCode{}, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /jwks", f.jwks)
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// SetSub sets the subject of the next ID tokens.
func (f *OIDCFake) SetSub(sub string) { f.mu.Lock(); f.sub = sub; f.mu.Unlock() }

// SetBadNonce makes ID tokens carry a nonce that does not match the request.
func (f *OIDCFake) SetBadNonce(v bool) { f.mu.Lock(); f.badNonce = v; f.mu.Unlock() }

// Apply fills the OIDC, session-secret and base URL fields of c so a Server or
// Auth talks to this fake and allows its current subject.
func (f *OIDCFake) Apply(c config.Config) config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret = f.URL, f.ClientID, config.Secret(f.ClientSecret)
	c.OIDCAllowedSubjects = []string{f.sub}
	c.SessionSecret = "test-session-secret-0123456789abcdef"
	if c.BaseURL == "" {
		c.BaseURL = "http://f95.test"
	}
	return c
}

func (f *OIDCFake) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token",
		"jwks_uri": f.URL + "/jwks", "response_types_supported": []string{"code"},
		"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"ES256"},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func (f *OIDCFake) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "ES256", Use: "sig"}}})
}

func (f *OIDCFake) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != f.ClientID || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("redirect_uri") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	code := base64.RawURLEncoding.EncodeToString(randBytes())
	f.mu.Lock()
	f.codes[code] = fakeCode{q.Get("code_challenge"), q.Get("nonce"), q.Get("redirect_uri")}
	f.mu.Unlock()
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *OIDCFake) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id != f.ClientID || secret != f.ClientSecret {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	f.mu.Lock()
	c, found := f.codes[r.PostFormValue("code")]
	delete(f.codes, r.PostFormValue("code")) // single use
	sub, bad := f.sub, f.badNonce
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if r.PostFormValue("grant_type") != "authorization_code" || !found || r.PostFormValue("redirect_uri") != c.redirect ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	nonce := c.nonce
	if bad {
		nonce = "wrong-nonce"
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: f.key, KeyID: "k1"}}, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now()
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": f.URL, "sub": sub, "aud": f.ClientID, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}).Serialize()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"access_token": "fake-access", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
}

func tokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randBytes() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

// LoginSession drives the full login flow against h (a web.Server handler
// configured with f.Apply) and returns the session cookie to attach to later
// requests: req.AddCookie(testutil.LoginSession(t, h, f)). The flow's last
// response is the callback; any non-session outcome fails the test.
func LoginSession(t testing.TB, h http.Handler, f *OIDCFake) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("LoginSession: /auth/login: %d %s", rec.Code, rec.Body.String())
	}
	pre := rec.Result().Cookies()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("LoginSession: authorize: %v", err)
	}
	resp.Body.Close()
	cb, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("LoginSession: authorize answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	req := httptest.NewRequest("GET", cb.RequestURI(), nil)
	for _, c := range pre {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "f95_session" && c.Value != "" {
			return c
		}
	}
	t.Fatalf("LoginSession: callback gave no session: %d %s", rec.Code, rec.Body.String())
	return nil
}
