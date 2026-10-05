# F95 Tracker: implementation spec

Status: consolidated from the resolved tickets of map [#1](https://github.com/jeiang/f95-tracker/issues/1). Build agents implement from this file; it assumes no other conversation context.

## 0. How to read this

- Terms follow [`CONTEXT.md`](../../CONTEXT.md) (Game, Source, Play status, Dev status, Game tag, Custom tag, Synonym, Tag review, Genre text, Play log, Behind, Update). Never use "status" unqualified.
- Schema, views, and numbered invariants live in [`data-model.md`](data-model.md) (final, #11). This file does not repeat the DDL; `INV-n` means invariant n there.
- Requirement ids are `R-<AREA>-<n>`. `#n` = the ticket whose resolution decided it (newer tickets override older ones on conflict). **PROPOSED** = invented here, not in any resolution; build agents may adjust it without asking, but must keep the observable contract of everything else.
- Research (facts, samples, not decisions): [F95 access](../research/f95-data-access.md), [Genre text](../research/f95-genre-text.md), [itch.io](../research/itch.md), [stack](../research/stack.md), [infra](../research/infra.md), [downloader](../research/downloader.md). Legion hosts were renamed after the infra research: **peria** = old legion-node4, **vida** = old legion-node2 (Pocket ID, NetBird server/proxy), **alda** = edge Caddy (#12). Use the new names everywhere.
- Timestamps: UTC ISO-8601 text (`2006-01-02T15:04:05Z`); dates `YYYY-MM-DD` (data-model conventions, #11). "Today"/"daily" boundaries use host-local time only for the 08:00 timer (#8); the daily-cap day is the host-local calendar day **PROPOSED**.

## 1. Scope and out of scope

In scope (Destination of #1): a single-user web app that tracks adult games from F95zone threads plus manual sources (itch.io best-effort, fanbox manual); daily Update check with an ntfy digest; Nix flake + NixOS module; downloader protocol and tracker-side API (#13); one-off CSV import (#10).

Out of scope (#1):
- All downloader work (tracker-side jobs, JSON API, downloads and paste pages, digest/push download sections, the worker, the userscript): later milestone M6 ([backlog](backlog.md)). v1 ships with no download features; the schema tables exist but stay unused. R-DL is the contract for M6.
- Multi-user support. A JavaScript backend.
- Not built, because [`data-model.md`](data-model.md) has no column for it: the itch.io `wharf/latest` per-Game channel opt-in mentioned as optional in #7. If wanted later it needs a migration.
- Using `api.f95checker.dev`, RSS, or `latest_data.php` (#2).
- Automating F95's masked-link captcha (#6, #13).

## 2. Architecture

### 2.1 Stack (#4)

| Concern | Choice |
|---|---|
| Language | Go 1.26 (`go.mod` stays at `go 1.26`; nixpkgs unstable has 1.26.8) |
| HTTP | stdlib `net/http` (method+path routing) |
| Templates | `github.com/a-h/templ` v0.3.1020; `templ.Fragment` for htmx partials |
| Frontend | server-rendered HTML, Tailwind v4 (`tailwindcss_4` 4.3.3 standalone CLI from nixpkgs), htmx 2.0.11, Alpine.js 3.17.4 (vendored via hash-pinned `fetchurl`, `go:embed`; no Node) |
| Auth | `github.com/coreos/go-oidc/v3` v3.21.0, `golang.org/x/oauth2` v0.37.0 (authorization code + S256 PKCE, confidential client) |
| Sessions | `github.com/alexedwards/scs/v2` v2.9.0 with its SQLite store (#11) |
| DB | `modernc.org/sqlite` v1.60.1 (pure Go, no cgo), `github.com/pressly/goose/v3` v3.28.0 (embedded migrations), `sqlc` v1.31.1 |
| Scraping | `github.com/PuerkitoBio/goquery` v1.13.0; ntfy and itch.io via stdlib `net/http` |
| Build | `buildGoModule` (`CGO_ENABLED=0`, `vendorHash`) |

Versions are pinned in `go.mod` / flake inputs; dev-tool versions in `devShells.default` must match (templ 0.3.1020, sqlc 1.31.1, goose 3.28.0 are in nixpkgs unstable at those versions, #4).

### 2.2 Processes

One binary `f95-tracker`, subcommands (full table in §4.2):

| Process | Runs as | Notes |
|---|---|---|
| `serve` | long-running systemd service `f95-tracker.service` | web UI, JSON API, `/healthz`; runs goose migrations at startup. Performs interactive F95 fetches (Add Game, Refresh) |
| `check` | `f95-tracker-check.service` oneshot fired by `f95-tracker-check.timer` (08:00 host-local, `Persistent=true`) (#8, #12) | the daily run (R-UPD) and digest (R-NOTIF) |
| `import-csv <file>` | manual CLI | one-time, idempotent (#10) |
| `fixture <thread>` | dev CLI | records scrubbed test fixtures (#18) |
| `backup <dest>` | CLI, used as the backup prepare step | online backup via `VACUUM INTO` (#12) |
| downloader worker | separate repo output `nixosModules.downloader` on artemis, **later milestone** | R-DL |

`serve` and `check` share one SQLite file (WAL). **PROPOSED**: `busy_timeout=5000`; one write connection pool (max 1 open) and a read pool; both processes always open through `internal/db`.

**PROPOSED, cross-process F95 pacing.** `serve` and `check` both talk to F95 and must respect the 5 s spacing together. Every F95 request takes an exclusive `flock` on `<stateDir>/f95.lock`, performs the request, and releases the lock 5 s after the request started (a goroutine sleeps out the remainder while holding it). Interactive callers wait up to 60 s for the lock, then report "F95 busy, try again". `check` takes a second lock `<stateDir>/check.lock` for its whole run so two runs never overlap (exit 0 with a log line if already running).

### 2.3 Package layout (PROPOSED)

Each build slice owns disjoint packages. Dependencies point downward: `web`/`api`/`cmd` → services → `db`/`domain`.

```
cmd/f95-tracker/            main + subcommand dispatch (serve, check, import-csv, fixture, backup, version)
internal/config/            flags/env/credentials parsing (§5.1); no other package reads os.Getenv
internal/clock/             Clock interface + fake (all "now" goes through it)
internal/domain/            enums (PlayStatus, DevStatus, Qualifier, Verification, JobState), Version normalizer, shared errors
internal/db/                Open (pragmas, pools), goose runner, Store.WithTx; hand-written glue only
internal/db/migrations/     00001_init.sql = data-model.md DDL (with the notification_item FK reorder), later migrations
internal/db/queries/        *.sql, one file per area (§5.2); sqlc output in internal/db/sqlcgen/ (committed)
internal/f95/               F95 HTTP client: limiter, cookie jar, retries, checker.php, thread-page parser, cookie probe, cookie-header parser
internal/itch/              itch.io page fetch + parser (Updated, title, uploads, version token, fingerprint)
internal/genre/             pure Genre-text parser, normalized keys, synonym application; no DB
internal/tags/              tag service: vocabulary growth, merge/refresh, add-time parse check model, Tag review, synonym re-apply
internal/games/             Game service: add/remove, Play status, rating, Play log, Sources, Behind/Update queries, list filters, covers cache
internal/check/             daily run orchestrator, detail-fetch queue worker, unavailable logic (R-UPD)
internal/notify/            ntfy client, digest builder, immediate pushes, notification bookkeeping
internal/csvimport/         CSV import (R-CSV)
internal/auth/              OIDC login, scs session wiring, allowlist, API-token issue/verify, middleware
internal/downloads/         download job service, state machine, mirror ordering, link ingestion
internal/web/               server wiring, router, middleware, page handlers, htmx partial handlers
internal/web/ui/            templ components and pages (§5.3)
internal/web/static/        embedded assets (app.css generated, htmx, Alpine, fonts)
internal/api/               JSON API handlers for downloader and userscript (R-DL, R-TOK)
internal/backup/            VACUUM INTO command
internal/fixture/           capture + scrub tool
internal/testutil/          httptest fakes (F95, itch.io, ntfy, OIDC), golden helper, DB fixture builder
testdata/                   committed scrubbed fixtures + golden JSON
nix/                        module.nix, downloader.nix, vm-test.nix, assets.nix
sqlc.yaml  flake.nix  .github/workflows/ci.yml
```

## 3. Behaviour by area

### 3.1 Auth and sessions (R-AUTH)

- **R-AUTH-1** (#12, #4) Login is in-app OIDC against Pocket ID (`https://auth.jeiang.dev`; issuer from config): authorization code + S256 PKCE, confidential client, `state` and `nonce` verified, ID token verified via issuer discovery. Redirect URI is `<baseUrl>/auth/callback`.
- **R-AUTH-2** (#12) Only subjects in `oidc.allowedSubjects` (module option, compared with the ID-token `sub`) get a session; any other authenticated subject gets 403. An empty allowlist denies everyone **PROPOSED** (the module asserts it is non-empty).
- **R-AUTH-3** (#12, #11) Session = scs row in the `sessions` table (revocable); 30-day sliding idle timeout. **PROPOSED**: absolute lifetime 365 d; cookie `f95_session`, `HttpOnly`, `Secure` when `baseUrl` is https, `SameSite=Lax`, `Path=/`.
- **R-AUTH-4** (#12) `sessionSecretFile` is mandatory config. **PROPOSED** use: HMAC key for the OIDC state/nonce/PKCE pre-login cookie and for the CSRF double-check below. (scs tokens themselves are opaque; #4 allowed "scs or signed cookie", #11 chose scs, #12 says "signed"; both are satisfied.)
- **R-AUTH-5** **PROPOSED** Unauthenticated requests: HTML GET → 302 `/auth/login?next=<path>` (same-origin relative paths only); htmx request (`HX-Request`) → `HX-Redirect: /auth/login`; `/api/*` → 401 JSON. Open without a session: `/healthz`, `/auth/*`, `/static/*`.
- **R-AUTH-6** **PROPOSED** `POST /auth/logout` deletes the session row; no RP-initiated logout. State-changing requests (non-GET/HEAD) from sessions go through `http.CrossOriginProtection` (Go 1.26 stdlib: rejects cross-origin by `Sec-Fetch-Site`/`Origin`); API routes with Bearer tokens are exempt.
- **R-AUTH-7** (#12) `/healthz` returns unauthenticated `200 ok` when the DB answers `SELECT 1`; used by Gatus and blackbox probes.

### 3.2 F95 client (R-F95)

Facts and samples: [F95 access research](../research/f95-data-access.md). Base URL `https://f95zone.to` (config override for tests only).

- **R-F95-1** (#2, #8) Endpoints used: `GET /sam/checker.php?threads=<id,id,…>` (≤100 ids, no cookie, JSON `{"status":"ok","msg":{"<id>":"<version>"}}`; 2 calls cover ~190 threads) and thread HTML `GET /threads/<id>/` (301 → slug, follow). Nothing else; never `latest_data.php`, RSS, or `api.f95checker.dev`.
- **R-F95-2** (#8) Cookie entry is a pasted raw `Cookie:` header (Settings). The app keeps only `xf_user`, `xf_tfa_trust`, `xf_session`, `xf_csrf` as `f95_credential.cookie_jar` (plaintext JSON by decision; state dir mode 0700 and DB file 0600 **PROPOSED** modes, service user only), plus the browser `User-Agent` of the pasting request in `user_agent`. Validated with one logged-in page load checking `data-logged-in="true"` (#8; the attribute sits on the `<html>` element **PROPOSED/INFERENCE**). Server-side requirement observed in #2: `xf_user` alone gets nginx 414; `xf_user`+`xf_tfa_trust` (or +`xf_session`) is logged in with no `cf_clearance`.
- **R-F95-3** (#8) Rotated `Set-Cookie` values from any F95 response are merged into the jar and persisted in the same request handling. The cookie is sent only on thread-page requests, never on `checker.php`, and never to the downloader or any other host (#2, #6, #13, INV-12).
- **R-F95-4** (#8) User-Agent: the stored browser UA on all F95 requests. **PROPOSED** fallback before any cookie is saved: `f95-tracker/<version> (+https://github.com/jeiang/f95-tracker)`; never a scraper-like UA (robots.txt, #2).
- **R-F95-5** (#8) Pacing: ≥5 s between F95 requests (shared limiter, §2.2). Routine detail-fetch cap: 40/day (R-UPD-6). Import backfill: 1 request / 5 s with its own budget (R-CSV-9).
- **R-F95-6** (#8, #2) Retry: on HTTP 429, 5xx, a challenge page, or the "temporarily blocked" JSON error, retry after 1 min, 5 min, 15 min; then stop all F95 requests for that run, set `check_run.f95_stopped=1`, and list "check failed" in the digest. Block signals to detect (strings from F95Checker, see research §3): titles `429 Too Many Requests` / `Error 429` / `DDoS-Guard` / `Just a moment...`, `_cf_chl_opt`, maintenance pages, JSON `"You have been temporarily blocked…"`. Interactive (web) requests do not sleep through the ladder: **PROPOSED** they report the failure once to the user and record it.
- **R-F95-7** **PROPOSED** Per-request timeout 30 s; response cap 5 MiB; `Accept-Encoding` default (gzip); no conditional GET (thread pages are `no-cache`, #2).
- **R-F95-8** (#2, #3, #8) Detail fetch (thread HTML, logged in) extracts: title (`h1.p-title-value`; name = title without prefix labels and trailing `[version] [developer]` groups; version = last-but-one bracket group), Dev status from prefix (Completed → completed, Onhold → on_hold, Abandoned → abandoned, none → ongoing), F95 tag list (`.js-tagList a.tagItem[href^="/tags/"]`, slug + label), Genre text (first post `article.message--post .bbWrapper`: `<b>Genre</b>` then sibling `.bbCodeSpoiler .bbCodeBlock-content`), "Thread Updated" date, cover URL (first `img.bbImage` in the OP, drop `/thumb/` for full size). Download-link extraction is **not** done by the tracker (links come from the user's browser, R-DL-3). F95 tags must be read logged in (guests miss some, #3).
- **R-F95-9** (#8) Version strings are compared by exact inequality only; never parsed or ordered (#2, INV-8). `checker.php` returns the title bracket verbatim (including a leading `v`).
- **R-F95-10** (#8, #2) Cookie invalid: a logged-in request that returns the login page (no `data-logged-in="true"`, or a 403 login page for a thread that previously loaded) sets `validity='invalid'` once; one immediate push (R-NOTIF-4); UI banner; detail fetches stay queued; Games added meanwhile get guest data and `details_pending=1`. Saving a new cookie re-validates and clears the banner.
- **R-F95-11** (#2, #8) Restricted/unavailable: a thread absent from the `checker.php` answer increments `miss_count`; see R-UPD-5. Threads 94891 and 151517 stay unreadable even logged in (403 generic); they are converted to manual Games at import (R-CSV-7).
- **R-F95-12** (#2) `xf_tfa_trust` expires (observed expiry 2026-10-26, lifetime otherwise unmeasured). `f95_credential.tfa_trust_expires_at` is an optional date field in the Settings cookie form **PROPOSED** (a pasted Cookie header carries no expiry); Settings and the list banner warn within 14 days of it **PROPOSED**.
- **R-F95-13** (#18) Parse sanity: a `checker.php` answer that is not `status: ok` or is empty for a non-empty request, or a thread page missing title, version, or the tag block (and, when logged in, the Genre block), is a parse failure: write `check_result(outcome='error')`, count it under Check failed in the digest, and do not overwrite stored data.
- **R-F95-14** (#14, #11) Cover: after a detail fetch, if `cover_source_url` changed or no file exists, download the image (not from the main host's `/attachments/`, which robots.txt disallows; use the `attachments.`/`preview.` host from the OP) and store it as `<stateDir>/covers/<gameId>.<ext>`; set `cover_path` (relative), `cover_source_url`, `cover_fetched_at`. **PROPOSED**: size cap 10 MiB, `image/*` only; failure leaves the old cover.

### 3.3 itch.io check (R-ITCH)

Facts: [itch.io research](../research/itch.md). Applies to Sources of kind `itchio` that are primary (#7, #14).

- **R-ITCH-1** (#7) Daily, one HTML GET of the game page per itch.io Game, ≥3 s apart, honest UA (`f95-tracker/<version>` **PROPOSED** string), no cookie. On 429, back off and retry via the same 1/5/15-min ladder as R-F95-6 (a separate stop flag is not needed: stop itch.io requests for the run, report under Check failed).
- **R-ITCH-2** (#7) Extract: `Updated` timestamp from `<td>Updated</td><td><abbr title="D Month YYYY @ HH:MM UTC">` → `thread_updated_at`; page `<title>`; upload names; highest `Version N` (`.version_name`).
- **R-ITCH-3** (#7, #11) `change_key` = fingerprint of (Updated timestamp, title, sorted upload names, highest `Version N`); any inequality is an Update. **PROPOSED** canonical form: `updated=<UTC>|title=<title>|uploads=<names joined by \x1f>|vmax=<N or ->`.
- **R-ITCH-4** (#7, #14) `latest_version` = a version token if found, else NULL (UI shows the `thread_updated_at` date). **PROPOSED** token heuristic: the last bracketed group of the title that is not (case-insensitively) `free`/`demo`/`pc`/`win`/`mac`/`linux`/`android`; else the first `v?\d+(\.\d+)+[a-z]?` or `ep\d+`/`ch\.?\s?\d+` match in the upload names; else NULL. A NULL token makes the Source date-only for Behind (data-model view).
- **R-ITCH-5** (#7) A page with no `Updated` row and no uploads (browser-only game) is not auto-trackable: **PROPOSED** the check sets `checks_enabled=0` on that Source and the UI shows "not trackable, update by hand"; its Dev status is user-editable like every non-F95 Source (R-UI-9).
- **R-ITCH-6** **PROPOSED** A 404/410 counts as a miss and follows the same 3-miss + confirming-fetch rule as F95 (R-UPD-5; the confirming fetch is the next day's identical request, so a third consecutive 404 marks unavailable).
- **R-ITCH-7** (#7) `devlog.rss` is used only to add the newest post title to the digest line of an updated itch.io Game; 404 means "no devlog", not an error. Optional: failure to fetch it never fails the check **PROPOSED**.
- **R-ITCH-8** (#7, #14) Fanbox and other manual Sources are never checked. Adding an itch.io Game fetches name (page `<title>`/`data.json` `title`) and the initial check values, then offers pasted tag text through the Genre parser (R-TAG-12).

### 3.4 Genre parser and tags (R-TAG)

Reference implementation and numbers: [Genre text research](../research/f95-genre-text.md) (`parse_genre.py`, `f95-tag-vocabulary.tsv`, `samples.jsonl`, synonym seed `SYN`).

- **R-TAG-1** (#9) Vocabulary: F95 tag slugs (`tag.kind='f95'`), auto-grown from every logged-in tag list fetched (unknown slug → insert; label from the anchor text), seeded from `f95-tag-vocabulary.tsv` in migration or startup seed **PROPOSED** (all 154 rows except `unknown`; asset-* tags may be included but are irrelevant). Custom tags (`kind='custom'`) for Genre phrases and hand-entered concepts with no F95 slug (BBW, Threesome, Elves…). Never delete tags.
- **R-TAG-2** (#9) Synonym table (`synonym`) editable in Settings, seeded from `SYN` (29 entries; the 2 explicit `None` entries are "known unmatched" and become no-match → custom tag, not seeded rows) (`origin='seed'`). Mapping fixes made by the user (parse check, Tag review, detail) are saved as `origin='user'` synonyms (#9, #14).
- **R-TAG-3** (#9) Parser rules 1–6 from the research, exactly: (1) split lines; blank lines and `[[spoiler]]` wrappers never reset the section; (2) heading `^(planned|future|possible|currently)…:` sets section qualifier (planned/future → planned, currently → present, possible → planned *flagged for confirmation*); initial section is present; text after the colon is the first item row; (3) strip bullets, tokenize on `,`, `. `, `...` outside parentheses, drop empties, trailing `.`, `for now`; (4) markers: a full parenthetical `(optional)`/`(avoidable)`/`(toggleable)`/`(can be disabled)` or a leading `optional ` → qualifier optional; other parentheticals are kept as a modifier note; phrases of more than 5 words with parentheses/prose go to manual; (5) normalized key = lowercase `[a-z0-9]` only; match key to vocabulary slug keys, then synonyms, then retry after stripping a leading `light|mild|soft` (that word becomes `modifier_note`), then split on `/` and match each side; (6) synonym seed. Output per item: raw phrase, matched tag or none, qualifier, modifier note, `needs_look` reason (synonym / no-match / prose / possible).
- **R-TAG-4** (#9, INV-13) Same tag present + planned → present; duplicates collapse (one row per Game and tag). Prose and long parentheticals are not turned into tags automatically (ignored by default in the parse check, R-UI-6).
- **R-TAG-5** (#9) Qualifiers are exactly present / planned / optional. Origins: `f95_list`, `genre`, `both`, `manual`. `f95_only=1` marks F95 tags absent from the Genre text (not "wrong", #3).
- **R-TAG-6** (#9, #14, INV-15) Verification changes only by the user: Tag review, by-hand edit of any tag, or by adding a tag by hand (starts `confirmed`, **any** Game: #14 extends #9, which limited it to manual Games). Add-time confirmation of the parse verifies nothing; every tag stays `unverified`. `confirmed`/`wrong` set `verified_at`. Tags marked `wrong` never match tag filters.
- **R-TAG-7** (#9, INV-16) Refresh = merge: user verifications and mapping overrides persist; new tags arrive `unverified` + `is_new`; tags gone from the Source keep their row with `removed_at_source_at` (visible and flagged until the user dismisses them by a review or by hand); a planned/optional tag that now appears present (Genre or F95 list) is promoted to present and flagged `promoted` for the next review; `f95_only` is recomputed. Tags with `origin='manual'` are never removed by refresh.
- **R-TAG-8** (#9, #14, INV-18) Tag review is offered on the **first** Play log entry the user adds for a Game (`origin='user'`); later entries ask only about tags new or changed since the last review (`is_new`, `promoted`, or created after `last_reviewed_play_log_id`'s entry). It lists all present tags (non-wrong, not removed at source is **PROPOSED**: removed-at-source tags are listed with their flag so they can be cleared); planned and optional tags sit in a separate collapsible section the user may skip. Prefill: a tag with an earlier verdict keeps it; otherwise a tag in both Genre text and the F95 list (`origin='both'`) prefills correct; the rest start unset (#9, overridden by #14).
- **R-TAG-9** (#14) Review actions: Save marks set verdicts (correct → `confirmed`, wrong → `wrong`); if every present tag now has a verdict, `tag_review.state='done'`, `reviewed_at` set, `last_reviewed_play_log_id` = the newest Play log entry, and `is_new`/`promoted` cleared; otherwise state stays `pending` (partial Save keeps the Game queued). Skip for now → `skipped` (no verdicts written). For planned/optional tags the choices are "now present" (qualifier → present, verification → confirmed) and "wrong".
- **R-TAG-10** (#14, #11, INV-18, INV-19) The queue is one row per Game (for the latest played version). Imported F95 Games start `pending` (R-CSV-8). Creating or editing a Synonym re-applies it to `unverified`, non-`mapping_override` tags on existing Games (re-point the tag, collapse duplicates by R-TAG-4); verified and overridden tags are untouched.
- **R-TAG-11** (#14) Tag filter semantics: **PROPOSED** included tags are ANDed, excluded tags ANDed-out (tap includes, tap again excludes, tap again clears); only `present` tags match by default; a toggle includes planned/optional; `verification='wrong'` and removed-at-source tags never match **PROPOSED for removed**.
- **R-TAG-12** (#9) Manual and itch.io Games: pasted tag text runs through the same parser to prefill; the user then edits by hand. Hand-picked tags start `confirmed`; pasted/parsed ones `unverified`.
- **R-TAG-13** (#9, #14) Add-time parse-check model (the data behind the Add Game page): `exact` matches (pre-accepted), `needs_look` items (synonym, no-match, prose, possible; each with editable target tag, qualifier, accept/ignore), and `f95_only` tags. Defaults on Confirm: exact and synonyms accepted; no-match phrases accepted as Custom tags **PROPOSED**; possible items accepted as planned **PROPOSED**; prose ignored; F95-only tags added as present, `unverified`, `f95_only=1`. Changing a mapping saves a Synonym (R-TAG-2).

### 3.5 Update and Behind (R-UPD)

Rule table with columns: [`data-model.md` § Update detection](data-model.md#update-detection).

- **R-UPD-1** (#8, #2) **Update** (F95) = exact string inequality between the stored `change_key` and the `checker.php` version. Behind compares last played vs latest with `lower`, trim, one leading `v` stripped (data-model views; the Go normalizer matches SQLite's ASCII-only `lower`/`trim`; one shared test-vector list, #11, #18).
- **R-UPD-2** (#8) Daily run order in `check`: (1) `checker.php` ×2 for all checkable F95 primaries (≤100 ids/call, no cookie); (2) itch.io page GETs **PROPOSED position**; (3) one logged-in cookie probe; (4) queued detail fetches; (5) digest. The run row is `check_run(kind='daily')`; every observation is a `check_result`.
- **R-UPD-3** (#11, data-model flow) Per primary Source answer: set `last_checked_at`, `miss_count=0`; if the key differs → `check_result(outcome='update', old_key, new_key)`, write new `latest_version`/`change_key`, enqueue `detail_fetch_queue(reason='update', budget='routine')`, and if the Game's Play status is in the alert set create a `download_job` in `awaiting_links` (trigger `update`, `target_version` = new version; R-DL-1) and record the Update for the digest. Non-alert Games still record the Update key (so a later status change does not alert on an old change) but produce no digest row, job, or badge.
- **R-UPD-4** (#10, INV-6) Import baseline: the live F95 version at import is stored as `latest_version`/`change_key` with no `check_result`, so pre-import changes never alert.
- **R-UPD-5** (#8, INV-10) Missing from `checker.php`: `miss_count+1`. At 3 consecutive misses do one confirming detail fetch; if the thread is gone/unreadable, set `unavailable_at` (+`unavailable_reason`), send one immediate push (R-NOTIF-4), and exclude the Source from checks until the user re-enables it (button on Game detail: clears `unavailable_at`, `miss_count`, sets `checks_enabled=1`). If the confirming fetch loads normally, reset `miss_count` **PROPOSED**.
- **R-UPD-6** (#8) Detail-fetch triggers: Game added, Update detected, weekly rolling refresh of primary F95 Sources whose Play status is not `finished`, the manual Refresh button, and import backfill. Queue = `detail_fetch_queue` (≤1 per Source). Routine cap 40/day counted as `check_result(step='detail')` rows of `check_run(kind='daily')` in the host-local day. **PROPOSED**: the weekly job enqueues, at the start of each daily run, Sources with `last_detail_at` older than 7 days (oldest first), at most 20 per run (~20/day); Add and manual-button fetches are executed immediately by `serve` (recorded as `check_run(kind='manual')`), bypass the cap, and still obey the 5 s limiter.
- **R-UPD-7** (#8) When the cookie is invalid, detail fetches stay in the queue untouched and resume after re-validation; the run still does steps 1, 2 and the digest.
- **R-UPD-8** (#10) Dev status: F95 live data wins and is overwritten on every detail fetch; non-F95 Sources keep the user-set value (INV-2). A Dev status change is reported in the Update's digest row (R-NOTIF-2).
- **R-UPD-9** (#14) **Update badge/filter** and **Behind badge** show only for Games whose Play status is in `alert_play_status` (default playing, on_hold, planned). Empty Play log is never Behind. **PROPOSED**: a Game has the Update badge while its primary Source has a `check_result(outcome='update')` newer than its newest `play_log.created_at` (so marking a version played clears it); the digest click-through filter (`/games?updates=1`) is exactly this set.
- **R-UPD-10** (#8, #11) Only primary Sources are checked; Sources with `checks_enabled=0`, `unavailable_at` set, or kind `manual` are skipped (`source_checkable` index).
- **R-UPD-11** (#8) `check` exit code: 0 even when F95/itch.io failed (failures are in the digest); non-zero only for infrastructure errors (config, DB, lock). **PROPOSED.**

### 3.6 CSV import (R-CSV)

Input `F95 Tracker - Sheet1.csv` (columns `Name,Version,Abandoned,Completed,On Hold,Rating,URL Code,Link`; `-` = empty). Evidence and merge rationale: #10.

- **R-CSV-1** (#10) `f95-tracker import-csv <file>`; one-time, **idempotent** by thread id (`UNIQUE(kind, external_id)`) or, for non-F95 rows, by `(kind, url)` **PROPOSED**; no UI. Re-running skips rows already imported and resumes the backfill (R-CSV-9).
- **R-CSV-2** (#10) Names: F95 thread title for F95 Games (from the backfill fetch; CSV name until then), CSV name for non-F95 Games.
- **R-CSV-3** (#10) Play log: one entry per CSV row from the CSV version, `played_on` NULL, `origin='imported'`. Rating: `-` → none; else 0.5–5 stored ×2.
- **R-CSV-4** (#10, #14) Play status: no version → `planned`; else Dev status completed/abandoned → `finished`; ongoing/on hold → `playing`. Dev status used is F95's live one for F95 Games, the CSV flags (Abandoned/Completed/On Hold; these record **Dev status**, not Play status, CONTEXT) only for non-F95 and converted rows. The prototype's own rules (rating ≤ 2 → dropped, etc.) are **not** used (#14). Every imported Game gets `import_review=1`. **PROPOSED**: until the backfill fetch gives the live Dev status, the Game carries a provisional status derived from CSV flags; the backfill re-derives it only while `import_review=1`.
- **R-CSV-5** (#10) Dev status on the primary Source: F95 live wins (CSV flags are stale, e.g. Furina flagged Abandoned but live v0.4a).
- **R-CSV-6** (#10, INV-5) Same-thread merges, one Game each: 64303 (Harem of the Princess + Rework), 70143 (Twisted World + Remake), 67937 (My Office Adventures 1.0 + the row misnamed "Reunion" at 1.00D1). Rule (general, applied to any duplicate thread id): the newer row supplies rating and last played version; the older row becomes an earlier Play log entry; "newer" = the row later in file order **PROPOSED** (rows are chronological). My Office Adventures Reunion (178717) stays separate.
- **R-CSV-7** (#10) Restricted threads 94891 (Lycoris Radiata) and 151517 (Ovulating Maiden) become Games with a `manual` primary Source (url = thread URL, CSV data, CSV Dev status). Non-F95 rows: 3 itch.io rows → `itchio` Source with best-effort check; fanbox (Little Green Hill) → `manual`; "Alchemy Shop" (itch.io browser game) → `itchio` with checks disabled per R-ITCH-5.
- **R-CSV-8** (#10) Tags: the backfill fetches the F95 tag list and Genre text for all F95 Games (logged in); all Game tags stay `unverified`; every F95 Game gets `tag_review.state='pending'`. No review runs during import.
- **R-CSV-9** (#8, #10) Backfill: after rows are written, the import writes baseline versions (R-UPD-4; one `checker.php` pass), enqueues `detail_fetch_queue(reason='import', budget='import')` for each F95 Game and drains it in-process at 1 request / 5 s as `check_run(kind='import_backfill')`. It pauses (exits with the queue intact, non-zero exit code **PROPOSED**) on any block signal or invalid cookie; re-running resumes. Flag `--no-backfill` **PROPOSED**.
- **R-CSV-10** (#14) The import review list (UI) shows every Game with `import_review=1`, the Play status derived and the rule that produced it (**PROPOSED**: text computed from Play log + Dev status at display time), and CSV flags shown as Dev status; filter by derived status, select rows, bulk-set Play status, confirm per row / selection / all shown. Confirming clears `import_review`.

### 3.7 Notifications (R-NOTIF)

- **R-NOTIF-1** (#12, #17) Delivery via ntfy at `ntfy.url`/`ntfy.topic` with a bearer token (`ntfy.tokenFile`): `POST <url>/<topic>`, body = message text, headers `Title`, `Priority`, `Click`, `Authorization: Bearer`. Self-hosted ntfy on peria, public through NetBird proxy service `ntfy.proxy.jeiang.dev`, `auth-default-access=deny-all`, `upstream-base-url=https://ntfy.sh` for iOS instant delivery (dotfiles work, R-DEP-8). Every attempt is a `notification` row; `sent_at` NULL + `error` on failure; unsent immediate pushes are retried at the start of the next `check` run **PROPOSED**; a failed digest is not re-sent.
- **R-NOTIF-2** (#17) Digest: sent at the end of the 08:00 run, only if there is something to report; priority 3; title `F95 Tracker: daily digest`; `Click` = `<baseUrl>/games?updates=1`; no action buttons. Sections in order, empty ones omitted: **Updates** (alert-set Games: name, old → new version, plus Dev status change if any); **Needs you** (F95 cookie invalid, download jobs awaiting links or needing a human, tags-to-review count); **Finished downloads**; **Check failed** (F95 or itch.io errors after retries).
- **R-NOTIF-3** (#17) Size: at most 20 item lines across the digest plus a final "and N more" so the body stays inline (ntfy 4,096-byte limit). **PROPOSED**: section headings and "and N more" are not counted; items are taken in section order; the body is additionally hard-truncated to 4,000 bytes at a line boundary.
- **R-NOTIF-4** (#17) Immediate pushes, priority 4, title `F95 Tracker: <event>`: F95 cookie turned invalid (once per invalidation, via `invalid_alerted_at`); Source marked unavailable (once, at the transition); download job needs a human (once per job, via `notification_item.download_job_id`). **PROPOSED** event names: `F95 cookie invalid`, `Source unavailable`, `Download needs you`. Check failures appear only in the digest.
- **R-NOTIF-5** **PROPOSED** Digest "news" test: send when the run has ≥1 new Update row, new finished download (done and not in an earlier digest), new Check failed entry, or a Needs-you item that is new since the last digest (job newly `awaiting_links`/`needs_human`); the tags-to-review count and an already-notified cookie-invalid state are appended only when the digest is sent for another reason. Rows covered are written as `notification_item`.
- **R-NOTIF-6** (#17) Download jobs awaiting links are listed in the digest with a link to the paste page (`<baseUrl>/downloads/<jobId>/links`), finished jobs appear in the next digest with no separate push (#13).

### 3.8 UI pages and behaviour (R-UI)

Source of truth: prototype branch `prototype/ui` (`prototype/README.md`, `assets/theme.css`, `assets/tailwind-config.js`, `screenshots/`) as amended by #14. The prototype is throwaway static HTML: re-implement in templ; reuse its tokens, primitives (`.btn`, `.field`, `.chip`, `.seg`, `.panel`) and page structure. Its "Open design questions" are answered by #14 below; its prototype chrome (striped bar, variant pill) is not shipped.

- **R-UI-1** (#14) Look: dark-first warm "ledger", amber accent, IBM Plex Sans (text) and Mono (versions, ids, dates), theme toggle (light/dark; **PROPOSED** persisted in `localStorage`, default dark). Colour tokens in `internal/web/static/theme.css` copied from the prototype. Phone: bottom tab bar replaces the sideways-scrolling nav (tabs: Games, Queue, Add, Import review (shown only while rows exist), Settings; **PROPOSED**). Fonts vendored and embedded, not loaded from a CDN **PROPOSED**.
- **R-UI-2** (#14) Game list `/games`: ledger table (variant A; stacked rows on a phone), default sort: Games with an Update or Behind first, then most recently updated at Source. Other sorts available (name, rating, Play status, last played, added) **PROPOSED**. Filters: name text, Play status, Dev status, tags (R-TAG-11), rating, Behind, Update available. Entry strips for the import review list and the tags-to-review queue. Per-Game badges: Update, Behind (alert-set Games only, R-UPD-9), details pending, Source unavailable, tags to review, check Play status (`import_review`). States: no-match, first-run empty. Filters apply via htmx (`hx-get` on the form, partial response replaces the table body, URL pushed).
- **R-UI-3** (#14, #11) Game detail `/games/{id}`: cover, name, Source link(s) (primary marked), Dev status, latest / last played / updated / last checked, **Refresh from Source** (htmx), Play status select with a hint saying whether that status alerts on Update, rating as tappable half-stars with a clear control (0.5–5, or none), Play log (editable date and version, deletable; imported entries shown "imported" and undated; deleting/editing recomputes last played), **Mark version played** (version prefilled with latest, date prefilled today, both editable, back-dating allowed; no "played without review" shortcut, Skip in the review suffices), Game tags grouped Present / Planned / Optional with ✓ confirmed / ✗ wrong / ○ unverified, origin label (Genre + F95, Genre, F95 only, by hand), modifier note, `new` / `removed at source` / `promoted` flags; set verification by hand on any tag; add a tag by hand (starts confirmed, R-TAG-6); raw Genre text; manage Sources (add link, change primary **PROPOSED**; only the primary is checked); **Download** button (R-DL-1); "Re-enable checks" for an unavailable Source (R-UPD-5).
- **R-UI-4** (#14, #9) Mark version played submits a Play log entry; if this is the Game's first user entry and it has present tags → redirect to the Tag review page; otherwise if tags are new/changed since the last review → offer the review; else back to the Game. Marking a version played never changes Play status by itself **PROPOSED**.
- **R-UI-5** (#14) Tag review `/games/{id}/review?version=…`: checklist (variant A) on every screen size; present tags with correct/wrong per tag; planned/optional in a collapsible, skippable section ("now present" / "wrong"); buttons **Save review** (back to the Game) and **Skip for now** (to the queue); partial Save keeps the Game queued (R-TAG-9).
- **R-UI-6** (#14, #9, #8) Add Game `/games/new`: source type F95 thread / itch.io / manual link; paste a URL or thread id; invalid input and already-tracked (link to existing Game) states. F95: fetch the thread (R-F95-8) and show the Genre text highlighted plus three panels — **Needs a look**, **Exact matches**, **F95 only** (model in R-TAG-13). Cookie invalid → add with details pending (guest data, no Genre text, `details_pending=1`, queued detail fetch), not blocked. Confirm is always enabled; states that no tag gets verified; Play status chosen on the page (default `planned` **PROPOSED**). Creating writes the baseline version (no Update), cover, and tags; **PROPOSED**: no `tag_review` row is created until the first Play log entry or a Skip, since the queue holds only Games whose review is skipped or pending. itch.io: fetch page (R-ITCH-8). Manual: name, URL, optional version, Dev status, paste tags.
- **R-UI-7** (#14, #10, #9) Tags-to-review queue `/queue`: Games with `tag_review.state IN ('pending','skipped')`, one row each, **Review now**; empty state.
- **R-UI-8** (#14, #10) Import review `/import-review`: R-CSV-10.
- **R-UI-9** (#14, #10, #7) Dev status is editable by hand only for non-F95 Sources (select on Game detail); for F95 it is read-only.
- **R-UI-10** (#14) Settings: see R-SET.
- **R-UI-11** **PROPOSED** Global banners (all pages): cookie invalid (link to Settings), `xf_tfa_trust` expiring (R-F95-12), last `check_run` failed/partial. Downloads page `/downloads`: list of jobs with state, mirror list, actions (cancel, mark done, retry), link to the paste page (R-DL-3).
- **R-UI-12** **PROPOSED** htmx conventions: partials are rendered with `templ.Fragment` from the same component as the full page; forms degrade to full-page POST/redirect/GET; error responses to htmx requests return a fragment with `HX-Retarget`/`HX-Reswap` to an inline error slot; Alpine only for local UI state (collapsibles, half-star hover, toggles).

### 3.9 Settings (R-SET)

- **R-SET-1** (#8, #14, #2) F95 cookie: paste the raw `Cookie:` header; **Save & check** (htmx) stores the jar + the request's UA, runs one validation load, shows `valid`/`invalid` and last checked. Optional `xf_tfa_trust` expiry field (R-F95-12).
- **R-SET-2** (#14, #11) Alert Play-status set (checkboxes over `alert_play_status`), showing how many Games are affected by each status; default playing, on hold, planned.
- **R-SET-3** (#9, #14) Synonym table editor: add, edit, remove, filter; edits re-apply per R-TAG-10.
- **R-SET-4** (#12, #14) ntfy: the server URL and topic are deploy-time config (module options), so Settings shows them **read-only** and offers **Send test notification** (htmx; posts a priority-3 `F95 Tracker: test`). (Resolves the prototype's editable ntfy fields in favour of #12 and the data model, which has no ntfy columns.)
- **R-SET-5** (#13, #11) API tokens (R-TOK) and downloader preferences: default mirror host order (`settings.mirror_host_order`, default pixeldrain > mega > gofile) and default platform preference (`settings.platform_pref`, default linux). **PROPOSED** UI for them lives here; per-Game override is on Game detail (`game.platform_pref`).

### 3.10 API tokens (R-TOK)

- **R-TOK-1** (#13, #11) Personal API tokens are created in Settings with a name and scope `submit_links` (userscript) or `downloader` (artemis worker); revocable; listed with `last_used_at`.
- **R-TOK-2** (#11, INV-24) Token = 32 random bytes; stored as `SHA-256` in `token_hash`; the plaintext is shown exactly once. **PROPOSED** wire format `f95t_<base64url(32 bytes)>`, sent as `Authorization: Bearer <token>`. A revoked token authenticates nothing. `last_used_at` updated on use (at most once per minute **PROPOSED**).
- **R-TOK-3** (#13, #12) The downloader token is also provisioned to artemis via sops (R-DEP-8); the `submit_links` token is pasted into the userscript by the user.
- **R-TOK-4** **PROPOSED** Scope enforcement: `submit_links` may call only the link endpoints; `downloader` only the downloader endpoints (§4.3). Neither can read Game data beyond what those endpoints return.

### 3.11 Downloader contract (R-DL; built in milestone M6, not v1)

Research: [downloader](../research/downloader.md). State machine: [`data-model.md`](data-model.md) notes.

- **R-DL-1** (#13) Job creation: an Update on a Game whose Play status is in the alert set creates a job in `awaiting_links` (listed in the digest, R-NOTIF-6); the Download button on Game detail creates one on demand (trigger `button`, `target_version` = current latest version). One open job per (Game, version) (INV-26; unique index). Creating when one is open returns the existing job.
- **R-DL-2** (#13, #11) States: `awaiting_links → queued → downloading → extracting → done`; fall-through `queued/downloading/extracting → queued`; any → `needs_human`; `needs_human → done | queued` (user action); `awaiting_links | queued | downloading | extracting | needs_human → cancelled` (user action; the worker stops on its next poll). `done`/`cancelled` are terminal. The transition function lives in `internal/downloads` (single function, no triggers) and writes `download_job_transition` in the same transaction.
- **R-DL-3** (#13, #6) Link hand-off: a Violentmonkey userscript on F95 thread pages collects the URLs the user has unmasked (after solving F95's captcha) and posts them with a `submit_links` token; a paste page `/downloads/<jobId>/links` is the fallback. The F95 cookie never leaves the user's browser for downloads. The tracker rejects masked URLs (`f95zone.to/masked/…`, **PROPOSED** 422 `masked_url`); masked URLs are never stored (INV-25). The userscript itself ships in the repo under `userscript/f95-tracker-links.user.js` **PROPOSED** (build task; its behaviour is only: unmask → collect → POST).
- **R-DL-4** (#13, #6) Job content: ordered mirror list (`download_mirror`); default host order pixeldrain > mega > gofile (`settings.mirror_host_order`); **supported hosts** = pixeldrain, mega, gofile; other hosts are kept as `supported=0, state='manual'` links. Platform preference Linux > Win/Linux > Win (`settings.platform_pref`, overridable per Game). **PROPOSED** ordering at ingestion: supported mirrors first, ordered by (platform rank per effective preference, host rank), then unsupported; `position` assigned in that order. Links do not expire.
- **R-DL-5** (#13) On link ingestion: ≥1 supported mirror → `awaiting_links → queued`; none supported (e.g. thread 12760's headline archive) → `needs_human` with reason "no supported mirror" + one push (R-NOTIF-4). If a thread has no open job, **PROPOSED** the submit creates one (trigger `button`) for the Game's current latest version, provided the Game is tracked (else 404).
- **R-DL-6** (#13) Worker behaviour (later milestone, spec only): polls the tracker every 60 s over NetBird via `f95.proxy.jeiang.dev` with a `downloader` bearer token; outbound only; reports progress and state; mirrors tried in order, a failed mirror falls through to the next; all mirrors failed → `needs_human` + push; the user can mark it done or drop the archive into `~/Games/.incoming` for extraction; downloads to `~/Games/.incoming`, verify, extract (zip / 7z / rar, multi-part) to `~/Games/<Game>/<version>`, delete the archive, keep all versions; per-host concurrency 1; resumable; pauses while gaming (`gaming.pauseUnits`). Automatable hosts: pixeldrain (REST), mega (MEGAcmd `mega-get`), gofile (`gallery-dl`) (#6).
- **R-DL-7** (#13, #17) Finished jobs appear in the next daily digest, no separate push; `needs_human` pushes immediately (R-NOTIF-4).
- **R-DL-8** (#13) artemis integration per infra research §8 (dotfiles work): static user, persisted state dir, `just migrate-persist` + `--boot` deploy for new persisted paths, backup allowlist entry, node-exporter unit regex (R-DEP-8).
- **R-DL-9** **PROPOSED** Directory name sanitizing for `<Game>`/`<version>` is the worker's job: replace `/`, NUL, and leading dots; keep the game name otherwise verbatim.

### 3.12 Deployment and NixOS (R-DEP)

Facts: [infra research](../research/infra.md) (dotfiles `cd8239b`, pre-rename), re-checked against dotfiles `main` `37488a7` in #12. Work in a temp checkout of `github:jeiang/.dotfiles` only; PR only; deploy only with the user's explicit approval (#12).

- **R-DEP-1** (#12) Host: `legion.services.f95-tracker` on **peria**. Durability: root-disk SQLite in the state dir, `backupSet` + hourly restic (atuin pattern). Loss budget 1 h.
- **R-DEP-2** (#12) Exposure: NetBird proxy HTTP service `f95.proxy.jeiang.dev` (defined in the NetBird dashboard, not Nix), **NetBird-Only Access**, CrowdSec, targeting peria over the mesh. No edge vhost. `baseUrl = https://f95.proxy.jeiang.dev`.
- **R-DEP-3** (#12) Scheduler: `systemd.timers.f95-tracker-check`, `OnCalendar` from `checks.schedule` (default 08:00 host-local daily), `Persistent=true`, oneshot `f95-tracker-check.service`.
- **R-DEP-4** (#12, #4) `nixosModules.default` defines `services.f95-tracker` with these options (names from #12; defaults **PROPOSED** unless noted):

| Option | Type / default |
|---|---|
| `enable` | bool |
| `package` | package, default `self.packages.<system>.default` |
| `user` / `group` | str, `f95-tracker` (static user, #12) |
| `host` | str, `127.0.0.1` (peria sets its mesh address; firewall scope is a `legion.services` field in dotfiles) |
| `port` | port, `8470` |
| `stateDir` | path, `/var/lib/f95-tracker` (via `StateDirectory`) |
| `baseUrl` | str, required |
| `oidc.issuer` | str, required (`https://auth.jeiang.dev`) |
| `oidc.clientId` | str, required (not a secret) |
| `oidc.clientSecretFile` | path, required (`LoadCredential`) |
| `oidc.allowedSubjects` | list of str, required non-empty (#12) |
| `sessionSecretFile` | path, required (`LoadCredential`) |
| `ntfy.url` | str, required |
| `ntfy.topic` | str, required |
| `ntfy.tokenFile` | path, required (`LoadCredential`) |
| `checks.schedule` | str (systemd calendar), `*-*-* 08:00:00` (#8) |
| `memoryMax` | str, `512M` |

  `#5`'s `memoryMaxMB`, `ntfy.topicFile`, `trustedProxies`, and `oidc.scopes/allowedGroups` are superseded by #12's list and are not options (the topic is not secret once ntfy is self-hosted with auth; client IP is not used).
- **R-DEP-5** (#12, #5) Secrets are `*File` paths loaded with systemd `LoadCredential` (never in the Nix store); the app reads `$CREDENTIALS_DIRECTORY/<name>` (§5.1). Hardening **PROPOSED**: `NoNewPrivileges`, `ProtectSystem=strict`, `ReadWritePaths=stateDir`, `PrivateTmp`, `ProtectHome`, `Restart=on-failure`, `MemoryMax`, `UMask=0077`.
- **R-DEP-6** (#12) `/healthz` (R-AUTH-7). Online backup command `f95-tracker backup <dest>` (`VACUUM INTO`); **PROPOSED** wiring: prepare step writes `<stateDir>/backup/f95-tracker.db`, restic backs up `stateDir`, and a restore oneshot runs when the live DB is missing, copying `backup/f95-tracker.db` into place before `serve` starts. Cached covers are in the restic set but refetchable.
- **R-DEP-7** (#12, #4) Flake outputs: `packages.default` (buildGoModule), `nixosModules.default`, `nixosModules.downloader`, `checks` (package build, go test/vet/staticcheck/gofmt, templ-generate diff, Tailwind build, `runNixOSTest` VM test), `devShells.default` (go, gopls, templ, sqlc, goose, air, tailwindcss_4, sqlite, gh). Flake input name in dotfiles: `f95-tracker`. **PROPOSED**: `packages.assets` is optional; `formatter` not required.
- **R-DEP-8** (#12, #13) Dotfiles backlog (PR tasks, each in a temp checkout): flake input; `legion.services` entry on peria; sops shard + `.sops.yaml` rule (session secret, OIDC client secret, ntfy token, downloader token on peria and artemis); `services.ntfy-sh` on peria (`auth-default-access=deny-all`, `upstream-base-url=https://ntfy.sh`) + proxy service; garret OIDC entry in `modules/garret`; gatus/monitoring (`/healthz`); topology; artemis `nixosModules.downloader` import, persistence, backup allowlist, `gaming.pauseUnits`. HITL checklists (the user does these): Pocket ID OIDC client (redirect `<baseUrl>/auth/callback`), NetBird dashboard services (`f95.proxy.jeiang.dev`, `ntfy.proxy.jeiang.dev`), secret values, F95 cookie paste, make the repo public. The repo becomes public at the end (needed for dotfiles to fetch it as an input).
- **R-DEP-9** (#13) `nixosModules.downloader` options **PROPOSED** (worker is later): `enable`, `package`, `user`/`group`, `trackerUrl`, `tokenFile`, `gamesDir` (default `~/Games` of the service user), `pollInterval` (default 60 s).
- **R-DEP-10** (#4) Build: `buildGoModule` with `env.CGO_ENABLED = 0`; Generated `*_templ.go` and sqlc output are committed and CI fails on drift (R-TEST-6) (#18; stack research left this open), so `preBuild` does not run `templ generate`; it builds Tailwind into `internal/web/static/app.css` (gitignored, generated in Nix and by a devShell `just assets` recipe) and uses the htmx 2.0.11 `dist/htmx.min.js`, Alpine.js 3.17.4 `dist/cdn.min.js` and IBM Plex fonts committed under `internal/web/static/` (built: vendored files in the repo instead of `fetchurl`).

### 3.13 Testing and CI (R-TEST)

- **R-TEST-1** (#18) Unit + golden tests (table-driven): F95 thread HTML (logged-in and guest), `checker.php` JSON, itch.io pages, Genre parsing against committed fixtures; parsed output compared to golden JSON; `-update` flag rewrites goldens. Seeded from the 42 Genre samples (`docs/research/f95-genre-text/samples.jsonl`).
- **R-TEST-2** (#18) `f95-tracker fixture <thread>` fetches with the stored cookie, strips csrf/masked-link tokens and user markers, fails if a known secret pattern remains (cookie values, `xf_` tokens, csrf, username) **PROPOSED patterns**; fixtures are reviewed in the PR diff.
- **R-TEST-3** (#18, #11) Shared normalizer vector list used by both the Go normalizer test and a SQL test of the `game_behind` view.
- **R-TEST-4** (#18) Integration: `httptest` fakes for F95, itch.io, ntfy, and OIDC (in `internal/testutil`) drive an end-to-end `check` run (Update detection, cookie probe, retries, unavailable after 3 misses, digest content).
- **R-TEST-5** (#18) NixOS VM test (`runNixOSTest`): boots `nixosModules.default` with a fake OIDC provider; asserts `/healthz`, the timer unit, state-dir ownership and modes, and the online backup command.
- **R-TEST-6** (#18, #12) CI: GitHub Actions runs `nix flake check` on every PR (go test, vet, staticcheck, gofmt, templ-generate diff, sqlc-generate diff **PROPOSED**, Tailwind build, VM test). Builds are pushed to garret on `main` only (needs the garret OIDC entry, R-DEP-8; push mechanics follow dotfiles' `modules/garret`).
- **R-TEST-7** (#18) Production drift: parse sanity failures (R-F95-13) are reported under Check failed and never overwrite stored data.
- **R-TEST-8** **PROPOSED** All time-dependent code takes `clock.Clock`; all F95/itch/ntfy base URLs and the pacing durations are injectable so tests run at full speed.

## 4. Interfaces

### 4.1 HTTP routes (PROPOSED unless noted)

Auth column: S = session, T(scope) = bearer token, – = open. htmx partials return fragments of the page component (R-UI-12); the same URL without `HX-Request` returns the full page.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` (#12) | – | 200 `ok` if DB answers |
| GET | `/auth/login` | – | start OIDC (PKCE) |
| GET | `/auth/callback` | – | finish OIDC, allowlist, create session |
| POST | `/auth/logout` | S | destroy session |
| GET | `/` | S | redirect `/games` |
| GET | `/games` | S | list; query: `q`, `play`, `dev`, `tag` (repeat; `-slug` excludes), `tags=all` (include planned/optional), `rating`, `behind=1`, `updates=1`, `sort`, `dir`; htmx → table fragment |
| GET | `/games/new` | S | Add Game page |
| POST | `/games/fetch` | S | add-time fetch + parse check (htmx fragment) |
| POST | `/games` | S | confirm and create Game |
| GET | `/games/{id}` | S | detail |
| POST | `/games/{id}/refresh` | S | detail fetch now (htmx) |
| POST | `/games/{id}/play-status` | S | set Play status |
| POST | `/games/{id}/rating` | S | set/clear rating (0.5 steps) |
| POST | `/games/{id}/platform` | S | per-Game platform preference |
| POST | `/games/{id}/play-log` | S | mark version played; redirects to review when due |
| POST | `/games/{id}/play-log/{entryId}` | S | edit entry (version, date) |
| POST | `/games/{id}/play-log/{entryId}/delete` | S | delete entry |
| POST | `/games/{id}/tags` | S | add a tag by hand (starts confirmed) |
| POST | `/games/{id}/tags/{tagId}` | S | set verification / qualifier / mapping |
| POST | `/games/{id}/sources` , `/games/{id}/sources/{sid}` | S | add link, set primary, edit Dev status (non-F95), enable checks |
| POST | `/games/{id}/download` | S | create download job |
| GET | `/games/{id}/review` | S | Tag review page (`?version=`) |
| POST | `/games/{id}/review` | S | save review (partial allowed) |
| POST | `/games/{id}/review/skip` | S | skip |
| GET | `/queue` | S | tags-to-review queue |
| GET | `/import-review` | S | import review list |
| POST | `/import-review` | S | bulk set Play status / confirm rows |
| GET | `/downloads` | S | job list |
| GET/POST | `/downloads/{jobId}/links` | S | paste page |
| POST | `/downloads/{jobId}/{action}` | S | `cancel`, `done`, `retry` |
| GET | `/covers/{gameId}` | S | cached cover file |
| GET | `/settings` | S | settings page |
| POST | `/settings/cookie` | S | Save & check cookie (htmx) |
| POST | `/settings/alert-set` | S | save alert set |
| POST | `/settings/downloads` | S | host order, default platform |
| POST/DELETE | `/settings/synonyms[/{id}]` | S | synonym CRUD |
| POST | `/settings/ntfy/test` | S | send test notification (htmx) |
| POST | `/settings/tokens` , `/settings/tokens/{id}/revoke` | S | create (shown once) / revoke token |
| GET | `/static/*` | – | embedded assets |
| GET | `/api/v1/downloader/jobs` | T(downloader) | open jobs for the worker (below) |
| POST | `/api/v1/downloader/jobs/{id}/events` | T(downloader) | progress/state report |
| GET | `/api/v1/links/pending?thread=<id>` | T(submit_links) | does the thread have a job awaiting links |
| POST | `/api/v1/links` | T(submit_links) | submit unmasked URLs |

JSON API contract (PROPOSED, all `application/json`, errors `{"error":"<code>","message":"…"}`):
- `GET /api/v1/downloader/jobs` → `{"jobs":[{"id","game":{"id","name"},"target_version","state","effective_platform_pref","mirrors":[{"id","position","host","url","platform","supported","state"}]}],"cancelled":[<job ids>]}`. `jobs` = states `queued`, `downloading`, `extracting`; `cancelled` = jobs cancelled in the last 7 days (the worker stops any local work for them). `awaiting_links` and `needs_human` are not returned.
- `POST …/events` body `{"state"?, "active_mirror_id"?, "mirror_id"?, "mirror_state"?, "error"?, "bytes_done"?, "bytes_total"?, "needs_human_reason"?, "result_path"?}`; the server validates via the state machine (409 `illegal_transition`); when the last supported mirror reports `failed` the server moves the job to `needs_human` ("all mirrors failed"). Actor recorded as `downloader`.
- `GET /api/v1/links/pending?thread=<id>` → `200 {"job_id","game_name","target_version"}` or `404`.
- `POST /api/v1/links` body `{"thread_id":<n>,"links":[{"url","platform"?,"label"?}]}` → `200 {"job_id","state","accepted":n,"manual":n}`; masked URLs → 422; unknown thread → 404. Host is derived from the URL's registrable domain (pixeldrain.com, mega.nz/mega.io, gofile.io = supported).
- No CORS: the userscript uses `GM_xmlhttpRequest`.

### 4.2 CLI (PROPOSED flags, subcommands from #4/#10/#12/#18)

| Command | Purpose |
|---|---|
| `f95-tracker serve` | web server; migrates DB at start |
| `f95-tracker check [--no-digest] [--only-checker]` | the daily run (#8); `--no-digest` for dev |
| `f95-tracker import-csv <file> [--no-backfill]` | R-CSV |
| `f95-tracker fixture <thread-id> [--out testdata/...]` | R-TEST-2 |
| `f95-tracker backup <dest>` | `VACUUM INTO <dest>` (fails if `dest` exists); prepare step for restic |
| `f95-tracker version` | build version |

Global flags: `--state-dir`, `--log-level`; every flag has an env equivalent (§5.1).

## 5. Cross-cutting contracts

### 5.1 Config, flags, env (PROPOSED)

`internal/config` is the only place that reads flags/env. Precedence: flag > env > default. Secrets never come from flags or plain env: they are files read from `$CREDENTIALS_DIRECTORY/<name>` (systemd `LoadCredential`), falling back to `F95_TRACKER_<NAME>_FILE` for dev.

| Setting | Flag / env | Default | Used by |
|---|---|---|---|
| state dir | `--state-dir` / `F95_TRACKER_STATE_DIR` | required | all (DB `f95-tracker.db`, `covers/`, `f95.lock`, `check.lock`, `backup/`) |
| listen | `--listen` / `F95_TRACKER_LISTEN` | `127.0.0.1:8470` | serve |
| base URL | `--base-url` / `F95_TRACKER_BASE_URL` | required for serve/check | OIDC redirect, ntfy Click, cookie Secure flag |
| OIDC issuer / client id / allowed subjects (comma list) | `F95_TRACKER_OIDC_ISSUER` / `_OIDC_CLIENT_ID` / `_OIDC_ALLOWED_SUBJECTS` | required for serve | auth |
| ntfy URL / topic | `F95_TRACKER_NTFY_URL` / `_NTFY_TOPIC` | required for serve/check | notify |
| credentials | `oidc-client-secret`, `session-secret`, `ntfy-token` | required | auth, notify |
| F95 base URL | `F95_TRACKER_F95_BASE_URL` | `https://f95zone.to` | f95 (tests/VM only) |
| log level | `--log-level` / `F95_TRACKER_LOG_LEVEL` | `info` | all |

The F95 cookie, UA, alert set, mirror preferences, synonyms, and tokens are runtime state in SQLite, **not** config (#1 Notes, #8).

### 5.2 DB access layer

- `sqlc.yaml` at the repo root: engine `sqlite`, schema = `internal/db/migrations`, queries = `internal/db/queries/*.sql`, output `internal/db/sqlcgen` (committed; CI checks drift). One query file per area: `games.sql`, `sources.sql`, `playlog.sql`, `tags.sql`, `review.sql`, `checks.sql`, `notify.sql`, `downloads.sql`, `auth.sql`, `settings.sql`. A slice adds its queries to its own file only; shared schema changes go through a new goose migration (never edit `00001_init.sql` after it merges).
- Migration `00001_init.sql` is the DDL in `data-model.md` with `download_job` ordered before `notification_item` and `notification_item.download_job_id REFERENCES download_job(id) ON DELETE SET NULL` (per the note there), plus the seeds: `settings` row, `alert_play_status` defaults (already in the DDL), tag vocabulary and synonym seed **PROPOSED** (generated from the research TSV and `SYN`).
- `internal/db.Store` exposes `Queries()` (read pool), `WithTx(ctx, func(*sqlcgen.Queries) error)` (the write connection; serializes writers; commits or rolls back), and nothing else. Services take `*db.Store`; no package outside `internal/db` imports `database/sql` drivers. Multi-statement domain operations (Update processing, review save, job transitions, merge) run in one `WithTx`.
- Enums are typed in `internal/domain`; invariants not expressible in SQL (state machine, normalizer) are enforced there.

### 5.3 Templates and components

- templ files live in `internal/web/ui/`, package `ui`, one file per page (`games_list.templ`, `game_detail.templ`, `tag_review.templ`, `add_game.templ`, `queue.templ`, `import_review.templ`, `settings.templ`, `downloads.templ`) plus `layout.templ` (`Layout(page PageMeta, body templ.Component)`: head, banners, nav, bottom tab bar) and `components.templ` (Badge, Chip, Seg, Panel, Button, Field, HalfStars, TagRow, VersionLabel, PlayStatusSelect, FormError). Generated `*_templ.go` is committed.
- Page components take plain view-model structs defined in `internal/web/ui/views.go` (no `sqlcgen` types), built by handlers in `internal/web`. Partials are `templ.Fragment("name")` blocks inside the page component, addressed by stable element ids (`#game-table`, `#tag-list`, `#play-log`, `#cookie-status`, `#parse-panels`, `#error-slot`).
- Styling: Tailwind v4 utility classes with the prototype's tokens; custom primitives (`.btn`, `.field`, `.chip`, `.seg`, `.panel`) in `internal/web/static/input.css` (`@import "tailwindcss"; @source "../**/*.templ";`). Build output `app.css` is generated, embedded with `go:embed`, cache-busted by content-hash query string.
- Vocabulary in UI text follows CONTEXT.md exactly (Play status, Dev status, Update, Behind, Tag review…).

### 5.4 Errors and logging

- Logging: `log/slog`, JSON to stderr (journald); fields `component`, `game_id`, `source_id`, `run_id` where relevant; never log cookie values, tokens, or Genre-fetched page bodies.
- Sentinel errors in `internal/domain`: `ErrNotFound`, `ErrConflict`, `ErrIllegalTransition`, `ErrValidation`; in `internal/f95`: `ErrCookieInvalid`, `ErrBlocked` (429/challenge/ratelimit), `ErrRestricted` (403 permission page), `ErrParse` (sanity failure, R-F95-13), `ErrBusy` (limiter lock timeout). Wrap with `%w`; handlers map them to HTTP codes (404/409/409/422; others 502 for F95 failures, 500 otherwise).
- User-facing errors on pages: inline error slot with a short sentence; no stack traces. API errors: JSON `{"error","message"}` (§4.1).
- Every external fetch outcome in a run is a `check_result` row; failures never abort the run for other Sources except `f95_stopped` (R-F95-6).

## 6. Resolution conflicts found while consolidating

| Conflict | Resolution |
|---|---|
| #5 default host legion-node4, public edge vhost on node1, ntfy.sh with secret `topicFile`, `memoryMaxMB`, `trustedProxies`, `oidc.scopes/allowedGroups` vs #12 | #12 wins: peria, NetBird proxy, self-hosted ntfy, option list per #12 (R-DEP-4). Host names updated to peria/vida/alda. |
| #9 hand-added tags start confirmed only on manual Games vs #14 any Game | #14 wins (R-TAG-6). |
| #9 review prefills only Genre∩F95 tags, earlier verdicts only a hint vs #14 earlier verdicts prefill | #14 wins (R-TAG-8). |
| #14 prototype default alert set playing + on hold vs #14 resolution / #11 playing + on hold + planned | The resolution text (three statuses) wins over the prototype README. |
| #4 sessions "scs or signed cookie" vs #11 scs SQLite table vs #12 "signed session cookie" | scs with SQLite store (#11); `sessionSecretFile` kept for HMAC of pre-login/CSRF state (R-AUTH-4). |
| Prototype Settings has editable ntfy server/topic vs #12 module options + data model with no ntfy columns | Read-only in UI, test button kept (R-SET-4). |
| #8 "browser UA for all F95 requests" vs #2 recommendation of a descriptive own UA | #8 wins; descriptive UA is only the no-cookie-yet fallback (R-F95-4). |
| #10 derives Play status from Dev status "F95 live wins", but live Dev status needs a detail fetch (not available at row write time) | Provisional CSV-flag derivation, re-derived by the backfill while `import_review=1` (R-CSV-4). |
| #13 "pixeldrain/mega/gofile only ever arrive masked" (#6) vs addendum: only pixeldrain and mega are masked in headline blocks | No effect on the contract; hosts are ordered/accepted as unmasked URLs from the browser either way. |
| #7 optional `wharf/latest` opt-in vs final data model without a column | Not built (§1). |
