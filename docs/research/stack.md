# Research: backend stack, Go vs Rust (ticket #4)

Date checked: 2026-10-04. Versions are "latest" as of that date from the primary registry named in each row. Anything not observed is marked `[INFERENCE]`. Terms follow `CONTEXT.md`.

## 1. Verified facts

### Pocket ID (the OIDC provider)
- Latest release v2.17.0; standard discovery at `/.well-known/openid-configuration`; endpoints `/authorize`, `/api/oidc/token`, `/api/oidc/userinfo`, `/.well-known/jwks.json`, `/api/oidc/end-session`. Scopes `openid profile email` (+ `groups`). Source: https://pocket-id.org/docs/setup/connect-an-app
- Confidential clients sign requests with a client secret; "public client" uses PKCE. In the server, `client.PkceEnabled = input.IsPublic || input.PkceEnabled` (PKCE is optional for confidential clients, mandatory for public). Source: https://github.com/pocket-id/pocket-id/blob/main/backend/internal/service/oidc_service.go (lines ~306-310).
- Discovery document advertises `grant_types_supported` = authorization_code, refresh_token, device_code, client_credentials; `response_types_supported` = `["code"]`; `code_challenge_methods_supported` = `["plain","S256"]`. Source: https://github.com/pocket-id/pocket-id/blob/main/backend/internal/controller/well_known_controller.go (lines ~109-117).
- Consequence: authorization code + PKCE (S256) with a confidential client is directly supported; any standards-compliant OIDC library works. No library-specific quirks documented. A new client lets nobody in until groups/"All Users" are chosen on its Access tab (connect-an-app page above).
- Pocket ID itself is written in Go and depends on `golang.org/x/oauth2` (pocket-id `backend/go.mod`, https://github.com/pocket-id/pocket-id/blob/main/backend/go.mod). Not proof of go-oidc compatibility, but the ecosystem match is real.

### Library versions (registries)
| Role | Go candidate | Rust candidate |
|---|---|---|
| HTTP | stdlib `net/http` (Go 1.22+ method/wildcard patterns); go.dev latest toolchain go1.27.1 (https://go.dev/VERSION?m=text) | axum 0.8.9 (2026-04-14) (https://crates.io/crates/axum) |
| HTML templates | templ v0.3.1020 (2026-05-10) (https://proxy.golang.org/github.com/a-h/templ/@latest); or stdlib `html/template` | askama 0.16.1 (2026-09-04), maud 0.27.0 (2025-02-02), minijinja 2.24.0 (2026-09-23) (crates.io API) |
| OIDC | coreos/go-oidc/v3 v3.21.0 (2026-09-01) + golang.org/x/oauth2 v0.37.0 | openidconnect 4.0.1 (2025-07-06); axum-oidc 0.6.0 (2026-05-15) |
| Sessions | alexedwards/scs/v2 v2.9.0 (2025-04-17) | tower-sessions 0.15.0; axum-login 0.18.0 |
| SQLite | modernc.org/sqlite v1.60.1 (2026-09-29, pure Go); mattn/go-sqlite3 v1.14.52 (cgo) | rusqlite 0.40.2; sqlx 0.9.0 |
| Migrations / queries | goose v3.28.0; sqlc v1.31.1 | refinery 0.10.0; sqlx migrate |
| HTML parsing | goquery v1.13.0 (2026-08-27) | scraper 0.27.0 |
| Scheduler (if in-process) | gocron/v2 v2.22.0 | tokio-cron-scheduler 0.15.1 |
| HTTP client (ntfy, F95) | stdlib `net/http` | reqwest 0.13.5 |
| Live reload | air v1.67.4; `templ generate --watch --proxy=... --cmd="go run ."` (https://templ.guide/developer-tools/live-reload/) | tower-livereload 0.10.3; cargo-watch |

Sources: crates.io API (`https://crates.io/api/v1/crates/<name>`), Go module proxy (`https://proxy.golang.org/<module>/@latest`), both queried 2026-10-04.

### Go directive / nixpkgs toolchain
- nixpkgs unstable ships Go 1.26.8 and rustc 1.98.1 (nixos MCP `info`, unstable channel).
- Dependency `go` directives: go-oidc 1.25.0, templ 1.25.0, modernc sqlite 1.26.0, x/oauth2 1.26.0, goose 1.26.0 (from `.mod` files on proxy.golang.org). All satisfied by nixpkgs Go 1.26.8, so the project's own `go.mod` must declare `go 1.26.x` (not 1.27) to build under nixpkgs unstable without toolchain download. Pin the flake to nixpkgs unstable (or bump when 1.27 lands).

### Frontend asset tooling in nixpkgs
- `tailwindcss_4` 4.3.3 is packaged as the **standalone CLI binary** (no Node): fetched from the tailwindlabs GitHub release for aarch64-darwin, aarch64-linux, x86_64-linux. Source: https://github.com/NixOS/nixpkgs/blob/master/pkgs/by-name/ta/tailwindcss_4/package.nix; version via nixos MCP.
- `templ` 0.3.1020, `sqlc` 1.31.1, `goose` 3.28.0 all in nixpkgs unstable (nixos MCP) at the same versions as the Go registry, so devShell tools match go.mod.
- htmx and Alpine.js are **not** in nixpkgs as JS packages (`alpinejs` NOT_FOUND; `htmx` resolves only to a Haskell package). Vendor them as pinned files instead:
  - htmx 2.0.11: `https://raw.githubusercontent.com/bigskysoftware/htmx/v2.0.11/dist/htmx.min.js` returned 200 (52 182 bytes); also in npm tarball `package/dist/htmx.min.js`.
  - Alpine 3.17.4: npm tarball `https://registry.npmjs.org/alpinejs/-/alpinejs-3.17.4.tgz` contains `package/dist/cdn.min.js` (verified). (The guessed GitHub raw path `packages/alpinejs/dist/cdn.min.js` is 404 because dist files are not committed.)
  - Fetching a tarball in Nix with `fetchurl`/`fetchzip` needs no npm or Node at all.
  - npm latest at check time: htmx.org 2.0.11, alpinejs 3.17.4, @tailwindcss/cli 4.3.3 (registry.npmjs.org).

### htmx partial rendering ergonomics
- templ: `@templ.Fragment("name") { ... }` renders only a subsection of a template, explicitly documented as an htmx optimisation (https://templ.guide/syntax-and-usage/fragments/). Components are plain Go functions returning `templ.Component`, so a handler renders the full page or a component depending on the `HX-Request` header.
- askama: has "Block fragments" (askama book `template_syntax.md`, https://github.com/askama-rs/askama/blob/master/book/src/template_syntax.md line ~503), similar capability.
- stdlib `html/template`: `{{define}}` + `ExecuteTemplate(w, "row", data)` gives named partials with no extra dependency, but no compile-time checking.

### Pure-Go SQLite
- mattn/go-sqlite3 is a cgo package requiring `CGO_ENABLED=1` and gcc (README, https://github.com/mattn/go-sqlite3). modernc.org/sqlite is a cgo-free transpilation, so `buildGoModule` can produce a static binary with `CGO_ENABLED=0` and no C toolchain; cross-building for NixOS on x86_64/aarch64 is trivial. [INFERENCE] on the cross-build point; the cgo-free property is from the module's documented design.
- rusqlite/sqlx need libsqlite3-sys, which by default builds bundled C or links system sqlite; fine in Nix but adds a C toolchain [INFERENCE].

## 2. Comparison

| Criterion | Go (net/http + templ + go-oidc + modernc sqlite + goose/sqlc + goquery) | Rust (axum + askama/maud + openidconnect + rusqlite/sqlx + refinery + scraper) |
|---|---|---|
| htmx partials | `templ.Fragment` documented for htmx; components are functions; HX-Request check is a one-liner | askama block fragments; maud/minijinja also fine |
| OIDC maturity, Pocket ID | go-oidc v3.21.0 (recent) + x/oauth2 `GenerateVerifier`/`S256ChallengeOption`/`VerifierOption` give PKCE in ~40 lines (https://github.com/golang/oauth2/blob/master/pkce.go); Pocket ID is itself a Go project using x/oauth2 | openidconnect 4.0.1 supports `PkceCodeChallenge` (docs.rs), last release 2025-07; `axum-oidc` 0.6.0 is young. Works against a spec-compliant server but typestate API is heavier |
| SQLite + migrations | modernc (cgo-free); goose with `embed.FS` SQL files run at startup; sqlc generates typed queries from SQL (matches CONTEXT model well) | rusqlite or sqlx (sqlx 0.9.0 compile-time-checked queries need a DB or offline `.sqlx` metadata in Nix build); refinery embedded migrations |
| HTML parsing | goquery (jQuery-style CSS selectors, built on x/net/html) | scraper (CSS selectors, html5ever); equal quality |
| Nix packaging | `buildGoModule` with `vendorHash`; one-line update by building once with a fake hash; `CGO_ENABLED=0` | crane or `buildRustPackage` with `cargoLock`/`cargoHash`; crane splits deps and app derivations for caching, which is more flake boilerplate |
| Build times | Go compiles in seconds-minutes, no separate dep derivation needed [INFERENCE] | Release build of axum+sqlx+reqwest+tls is typically minutes; crane caches deps [INFERENCE] |
| Tailwind without Node | `tailwindcss_4` in nixpkgs, same for both | same |
| Dev loop | `air` or `templ generate --watch --proxy` (templ.guide live-reload page); fast rebuild | cargo-watch + tower-livereload; slower incremental builds [INFERENCE] |
| Testability | stdlib `httptest`, interface-based fakes; goquery tests against saved F95 HTML fixtures; sqlite in-memory | `tower::ServiceExt::oneshot`; equally testable, more ceremony |
| Daily job | systemd timer calling a subcommand of the same binary, or gocron in-process | systemd timer or tokio-cron-scheduler |
| Fit for single-user hobby app | minimal dependency count, few concepts | stronger type guarantees but higher cost per change |

## 3. Chosen stack: Go

Reasons: (1) the PKCE flow against Pocket ID is straightforward with maintained, current libraries (go-oidc 3.21.0, x/oauth2 0.37.0) and Pocket ID advertises code + S256; (2) templ has first-class documented fragment support for htmx; (3) modernc sqlite removes cgo so `buildGoModule` stays simple with no C toolchain; (4) templ/sqlc/goose/tailwindcss_4 are all at current versions in nixpkgs so the devShell matches go.mod; (5) fastest edit-build loop and simplest Nix packaging for a one-person project; (6) heavy type machinery of Rust buys little here (CRUD + scraper + notifier).

Library list:
- Runtime: Go 1.26 (nixpkgs unstable 1.26.8); `net/http` ServeMux (method + path patterns); `log/slog`.
- Templates: `github.com/a-h/templ` v0.3.1020 (generated `*_templ.go` committed or generated in the Nix build, see below).
- Auth: `github.com/coreos/go-oidc/v3` v3.21.0, `golang.org/x/oauth2` v0.37.0 (authorization code + S256 PKCE, confidential client; `state` and `nonce` verified; ID token verified via issuer discovery). Sessions: `github.com/alexedwards/scs/v2` v2.9.0 with its SQLite store, or a signed cookie holding only the OIDC `sub`; single user, so an `ALLOWED_SUB` (or email) config check after login is enough [INFERENCE on scs store specifics; verify when implementing].
- DB: `modernc.org/sqlite` v1.60.1 via `database/sql`; `github.com/pressly/goose/v3` v3.28.0 (embedded SQL migrations applied on startup); `sqlc` v1.31.1 (dev-time codegen, generated code committed).
- Scraping: `github.com/PuerkitoBio/goquery` v1.13.0; stdlib `net/http` client for F95zone, itch.io, and ntfy (a plain POST to the ntfy topic URL).
- Scheduler: recommended systemd timer running `f95-tracker check` (same binary, new subcommand) from the NixOS module, so a failed run is visible in `journalctl` and the web process stays stateless. In-process (`gocron/v2` v2.22.0) remains a fallback; this decision is out of scope for this ticket beyond the module shape below.
- Dev tools: `templ`, `sqlc`, `goose`, `air`, `tailwindcss_4` from nixpkgs.

## 4. Nix packaging outline

```
flake.nix outputs (per system: x86_64-linux, aarch64-linux, aarch64-darwin for dev):
  packages.default      = f95-tracker (buildGoModule)
  packages.assets       = tailwind css + vendored js (optional split)
  nixosModules.default  = services.f95-tracker
  devShells.default
  checks.default        = package build (+ go vet via buildGoModule checkPhase)
```

`packages.default` (sketch):
```nix
buildGoModule {
  pname = "f95-tracker"; version = "0.0.0";
  src = ./.;
  vendorHash = "sha256-...";          # set lib.fakeHash first, copy hash from error
  env.CGO_ENABLED = 0;                # modernc sqlite: no gcc
  nativeBuildInputs = [ templ tailwindcss_4 ];
  preBuild = ''
    templ generate
    tailwindcss -i web/input.css -o web/static/app.css --minify
    cp ${htmx}/htmx.min.js ${alpine}/cdn.min.js web/static/
  '';
  # web/static is //go:embed'd so the binary is self-contained
}
```
- `htmx = fetchurl { url = ".../htmx.org-2.0.11.tgz"; hash = ...; }` unpacked (or `fetchurl` of the raw `dist/htmx.min.js` at the `v2.0.11` tag); `alpine = fetchzip` of `alpinejs-3.17.4.tgz`, using `package/dist/cdn.min.js`. Pinned by hash, no npm.
- Generated `*_templ.go` and sqlc output: decide at implementation; generating `templ` in `preBuild` keeps the repo clean but makes `go build` outside Nix require `templ generate`; committing generated sources avoids that. Recommended: commit sqlc output (needs no extra tool at build), generate templ in Nix and in the devShell.
- `vendorHash` changes whenever `go.mod`/`go.sum` change; document `nix-update` or "set fakeHash and rebuild" in README.

`nixosModules.default` (`services.f95-tracker`): options `enable`, `package`, `listenAddress`/`port`, `baseUrl`, `dataDir` (default `/var/lib/f95-tracker`, via `StateDirectory`), `oidc.issuer`, `oidc.clientId`, `oidc.clientSecretFile` (path loaded via `LoadCredential`, never in the Nix store), `ntfy.url`/`ntfy.tokenFile`, `checkSchedule` (default `daily`), `alertPlayStatuses`. Defines `systemd.services.f95-tracker` (DynamicUser, hardening, `StateDirectory`) and `systemd.services.f95-tracker-check` + `systemd.timers.f95-tracker-check` (`OnCalendar`, `Persistent=true`, `RandomizedDelaySec`). Secrets as file paths, per nixpkgs convention.

`devShells.default`: `go`, `gopls`, `templ`, `sqlc`, `goose`, `air`, `tailwindcss_4`, `sqlite`, `gh`; shellHook prints commands. `air` config runs `templ generate`, tailwind `--watch` in a second process, and `go build`. Alternative: `templ generate --watch --proxy=http://localhost:8080 --cmd="go run ."` for browser auto-reload (templ.guide).

## 5. Asset pipeline (no Node)

1. `tailwindcss_4` standalone CLI from nixpkgs (verified a binary release, no Node). v4 uses CSS-first config (`@import "tailwindcss"; @source "../**/*.templ";`) [INFERENCE from Tailwind v4 docs, not re-fetched]; scanning `.templ` files for class names.
2. htmx and Alpine vendored as hash-pinned fetches in the flake (URLs above); copied to `web/static/` at build time. For `go run` dev outside Nix, the devShell hook copies them (or a `just assets` recipe).
3. Everything embedded with `//go:embed web/static` and served with `http.FileServerFS`; cache-busting by content hash query string or filename.
4. No `package.json`, no `node_modules`.

## 6. Risks / unresolved
- Build-time comparison numbers are `[INFERENCE]`; no benchmarks were run (research-only constraints).
- go-oidc against Pocket ID not exercised here; Pocket ID's discovery and token behavior is standard, so risk is low. Smoke test during implementation.
- nixpkgs Go (1.26.8) lags go.dev latest (1.27.1); pin `go 1.26` in go.mod.
- `scs` last released 2025-04; stable and tiny, but if undesired use a signed cookie.

## Recommendation

Use **Go 1.26** with stdlib `net/http` routing, **templ v0.3.1020** (with `templ.Fragment` for htmx partials), **coreos/go-oidc/v3 v3.21.0 + golang.org/x/oauth2 v0.37.0** for authorization code + S256 PKCE against Pocket ID (confidential client; Pocket ID advertises `code` response type and S256), **modernc.org/sqlite v1.60.1** (pure Go, no cgo) with **goose v3.28.0** embedded migrations and **sqlc v1.31.1**, **goquery v1.13.0** for F95zone/itch.io parsing, stdlib HTTP client for ntfy, **scs v2.9.0** (or signed cookie) for the single-user session. Run the daily Update check as a `check` subcommand triggered by a systemd timer from the NixOS module. Package via `buildGoModule` (`CGO_ENABLED=0`, `vendorHash`), flake outputs `packages.default`, `nixosModules.default` (`services.f95-tracker`), `devShells.default`. Assets: `tailwindcss_4` 4.3.3 standalone CLI from nixpkgs; htmx 2.0.11 and Alpine 3.17.4 vendored as hash-pinned `fetchurl` files (npm tarballs or raw dist), embedded via `go:embed`; no Node anywhere.
