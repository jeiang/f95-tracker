// Package auth is in-app OIDC login (authorization code + S256 PKCE) and the
// scs session over the sessions table (R-AUTH-1..6). It has no HTTP routes or
// pages; internal/web maps its results and errors onto responses.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
)

const (
	SessionCookie  = "f95_session"
	preLoginCookie = "f95_oidc"
	sessionSubKey  = "sub"
	preLoginTTL    = 10 * time.Minute
	httpTimeout    = 10 * time.Second
)

var (
	// ErrInvalid: the callback does not match a login this browser started
	// (missing/forged/expired cookie, state or nonce mismatch, IdP error).
	ErrInvalid = errors.New("invalid sign-in response")
	// ErrForbidden: authenticated at the IdP but the subject is not allowlisted.
	ErrForbidden = errors.New("subject not allowed")
	// ErrProvider: the OIDC provider could not be reached or rejected the exchange.
	ErrProvider = errors.New("identity provider unavailable")
)

type Auth struct {
	sm       *scs.SessionManager
	store    sessionStore
	cfg      config.Config
	clock    clock.Clock
	log      *slog.Logger
	secure   bool
	secret   []byte
	httpc    *http.Client
	mu       sync.Mutex
	provider *oidc.Provider
}

func New(cfg config.Config, store *db.Store, clk clock.Clock, log *slog.Logger) *Auth {
	ss := sessionStore{db: store}
	sm := scs.New()
	sm.Store = ss
	sm.IdleTimeout = 30 * 24 * time.Hour
	sm.Lifetime = 365 * 24 * time.Hour
	secure := strings.HasPrefix(cfg.BaseURL, "https://")
	sm.Cookie.Name = SessionCookie
	sm.Cookie.Path = "/"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = secure
	sm.Cookie.SameSite = http.SameSiteLaxMode
	return &Auth{
		sm: sm, store: ss, cfg: cfg, clock: clk, log: log.With("component", "auth"), secure: secure,
		secret: []byte(cfg.SessionSecret.Reveal()), httpc: &http.Client{Timeout: httpTimeout},
	}
}

// Middleware loads and saves the session around next.
func (a *Auth) Middleware(next http.Handler) http.Handler { return a.sm.LoadAndSave(next) }

// Authenticated reports whether the request carries a valid session. It needs
// Middleware to be upstream.
func (a *Auth) Authenticated(r *http.Request) bool {
	return a.sm.GetString(r.Context(), sessionSubKey) != ""
}

// Logout deletes the session row and clears the cookie.
func (a *Auth) Logout(r *http.Request) error { return a.sm.Destroy(r.Context()) }

// SafeNext returns next if it is a same-origin relative path, else "/".
func SafeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, `\`) {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return next
}

type preLogin struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Expires  int64  `json:"e"`
}

func (a *Auth) sign(msg []byte) []byte {
	m := hmac.New(sha256.New, a.secret)
	m.Write(msg)
	return m.Sum(nil)
}

func (a *Auth) setPreLogin(w http.ResponseWriter, p preLogin) {
	b, _ := json.Marshal(p)
	v := base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.sign(b))
	http.SetCookie(w, a.preLoginCookie(v, int(preLoginTTL.Seconds())))
}

func (a *Auth) preLoginCookie(v string, maxAge int) *http.Cookie {
	// Path /auth/callback keeps it off every other request; Lax still sends it on
	// the top-level redirect back from the IdP.
	return &http.Cookie{Name: preLoginCookie, Value: v, Path: "/auth/callback", MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode}
}

func (a *Auth) readPreLogin(r *http.Request) (preLogin, error) {
	var p preLogin
	c, err := r.Cookie(preLoginCookie)
	if err != nil {
		return p, ErrInvalid
	}
	body, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return p, ErrInvalid
	}
	b, err1 := base64.RawURLEncoding.DecodeString(body)
	s, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil || !hmac.Equal(s, a.sign(b)) || json.Unmarshal(b, &p) != nil {
		return p, ErrInvalid
	}
	if a.clock.Now().Unix() > p.Expires {
		return p, ErrInvalid
	}
	return p, nil
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *Auth) oidcContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, a.httpc)
}

// discover resolves the provider on first use and caches it, so the server
// starts while the IdP is down.
func (a *Auth) discover(ctx context.Context) (*oidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provider != nil {
		return a.provider, nil
	}
	p, err := oidc.NewProvider(a.oidcContext(ctx), a.cfg.OIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("%w: discovery: %v", ErrProvider, err)
	}
	a.provider = p
	return p, nil
}

func (a *Auth) oauthConfig(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID: a.cfg.OIDCClientID, ClientSecret: a.cfg.OIDCClientSecret.Reveal(),
		Endpoint: p.Endpoint(), RedirectURL: a.cfg.BaseURL + "/auth/callback", Scopes: []string{oidc.ScopeOpenID},
	}
}

// BeginLogin sets the pre-login cookie and returns the IdP authorization URL.
func (a *Auth) BeginLogin(ctx context.Context, w http.ResponseWriter, next string) (string, error) {
	p, err := a.discover(ctx)
	if err != nil {
		return "", err
	}
	pl := preLogin{State: randomToken(), Nonce: randomToken(), Verifier: oauth2.GenerateVerifier(),
		Next: SafeNext(next), Expires: a.clock.Now().Add(preLoginTTL).Unix()}
	a.setPreLogin(w, pl)
	return a.oauthConfig(p).AuthCodeURL(pl.State, oauth2.S256ChallengeOption(pl.Verifier), oidc.Nonce(pl.Nonce)), nil
}

// CompleteLogin finishes the callback: verifies cookie, state, code exchange,
// ID token and nonce, checks the allowlist and starts a fresh session. It
// returns the (safe) path to send the user to.
func (a *Auth) CompleteLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	pl, err := a.readPreLogin(r)
	http.SetCookie(w, a.preLoginCookie("", -1))
	if err != nil {
		return "", err
	}
	q := r.URL.Query()
	if q.Get("error") != "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(pl.State)) != 1 || q.Get("code") == "" {
		return "", ErrInvalid
	}
	ctx := a.oidcContext(r.Context())
	p, err := a.discover(ctx)
	if err != nil {
		return "", err
	}
	oc := a.oauthConfig(p)
	tok, err := oc.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pl.Verifier))
	if err != nil {
		return "", fmt.Errorf("%w: token exchange: %v", ErrProvider, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return "", fmt.Errorf("%w: no id_token", ErrProvider)
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: a.cfg.OIDCClientID}).Verify(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("%w: id_token: %v", ErrInvalid, err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pl.Nonce)) != 1 {
		return "", ErrInvalid
	}
	if idt.Subject == "" || !slices.Contains(a.cfg.OIDCAllowedSubjects, idt.Subject) {
		a.log.Warn("login denied", "sub", idt.Subject)
		return "", ErrForbidden
	}
	if err := a.sm.RenewToken(r.Context()); err != nil {
		return "", err
	}
	a.sm.Put(r.Context(), sessionSubKey, idt.Subject)
	if err := a.store.deleteExpired(r.Context()); err != nil {
		a.log.Warn("delete expired sessions", "err", err)
	}
	return pl.Next, nil
}
