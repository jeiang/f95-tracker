package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/testutil"
)

const rawPage = `<!DOCTYPE html><html data-logged-in="true" data-csrf="1234567890,0123456789abcdef0123456789abcdef">
<head><title>T [v1] [d]</title><meta property="og:description" content="secret-ish"><script>var csrf='1234567890,0123456789abcdef0123456789abcdef'; XF.config={userId: 4242424}</script>
<script>jQuery.extend(true, XF.config, { userId: 4242424, csrf: '1234567890,0123456789abcdef0123456789abcdef' })</script></head>
<body>
<div class="p-nav"><span class="p-navgroup-linkText">testuser</span></div>
<header class="x"><span class="avatar" data-user-id="4242424"><img src="/data/avatars/s/4242/4242424.jpg?1594449079" alt="testuser" class="avatar-u4242424-s"></span></header>
<a href="/members/testuser.4242424/" class="u">Profile of testuser</a>
<span class="badge" data-badge="7">7</span>
<form><input type="hidden" name="_xfToken" value="1234567890,0123456789abcdef0123456789abcdef"></form>
<article class="message--post"><div class="bbWrapper"><b>Genre</b>:<br>
<a href="https://f95zone.to/masked/mega.nz/67494/4242424/AAAAAAAAAAAAAAAAAAAAAAAAAAA/BBBBBBBBBBBBBBBBBBBBBB/CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC">MEGA</a>
cookie echo: xf_user=cookievalue123456; id 24242424 stays</div></article>
<article class="message--post"><div class="bbWrapper">reply by someone</div></article>
</body></html>`

func TestScrubRemovesSecretsAndKeepsContent(t *testing.T) {
	out, err := Scrub([]byte(rawPage), "cookievalue123456")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{
		"0123456789abcdef0123456789abcdef", "1234567890", "testuser", "cookievalue123456",
		"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "data-badge=\"7\"", "reply by someone", "<script", "og:description",
	} {
		if strings.Contains(s, bad) {
			t.Errorf("scrubbed page still contains %q", bad)
		}
	}
	// the viewer's id goes away but an unrelated number that merely contains it stays
	if strings.Contains(strings.ReplaceAll(s, "24242424", ""), "4242424") || !strings.Contains(s, "24242424") {
		t.Errorf("user id handling wrong")
	}
	for _, keep := range []string{`data-logged-in="true"`, "<title>T [v1] [d]</title>", "<b>Genre</b>", "/masked/mega.nz/67494/", "data-badge=\"0\""} {
		if !strings.Contains(s, keep) {
			t.Errorf("scrubbed page lost %q", keep)
		}
	}
}

func TestVerifyRejectsLeftovers(t *testing.T) {
	for name, page := range map[string]string{
		"csrf token":     `<p>1234567890,0123456789abcdef0123456789abcdef</p>`,
		"xfToken":        `<input name="_xfToken" value="abc">`,
		"data-csrf":      `<html data-csrf="abc">`,
		"masked":         `<a href="https://f95zone.to/masked/mega.nz/1/2/tok">x</a>`,
		"cookie":         `Cookie: xf_session=abcdef`,
		"user id":        `userId: 42,`,
		"literal secret": `hello Testuser`,
	} {
		if err := Verify([]byte(page), "testuser"); err == nil {
			t.Errorf("%s: Verify accepted a page with a secret", name)
		}
	}
	if err := Verify([]byte(`<html data-csrf="SCRUBBED">userId: 0,`), "testuser"); err != nil {
		t.Errorf("clean page rejected: %v", err)
	}
}

func TestRecordScrubsWithStoredCookie(t *testing.T) {
	fake := testutil.NewF95Fake(t)
	page := `<html data-logged-in="true"><head><title>x [v1] [d]</title></head><body><article class="message--post">echo user1-secret-cookie</article></body></html>`
	fake.SetThread("5", []byte(page), []byte("guest"))
	store := testutil.NewStore(t)
	creds := f95.NewCredStore(store, clock.NewFake(time.Now()))
	c, err := f95.New(f95.Options{BaseURL: fake.URL(), Creds: creds})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Record(ctx, c, creds, "5"); err == nil {
		t.Fatal("recording without a stored cookie must fail")
	}
	if err := creds.Replace(ctx, f95.Jar{"xf_user": "user1-secret-cookie", "xf_session": "s"}, "ua", time.Time{}); err != nil {
		t.Fatal(err)
	}
	out, err := Record(ctx, c, creds, "5")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "user1-secret-cookie") || !strings.Contains(string(out), "echo") {
		t.Errorf("recorded page = %s", out)
	}
}
