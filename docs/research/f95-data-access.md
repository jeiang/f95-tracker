# F95zone data access and update detection (ticket #2)

Method: live probes from this machine on 2026-10-04 with plain `curl` (custom UA `f95-tracker-research`, no cookies, >=3 s between F95 requests, ~25 F95 requests total), plus reading the source of WillyJL/F95Checker (shallow clone, `modules/api.py`, `indexer/*.py`, `common/parser.py`, `README.md`). Terms: `/help/terms/`, `/help/cookies/`, `/robots.txt`.
Raw samples: `docs/research/f95-data-access/`.

**Limitation: the logged-in browser relay was not available** (the omp relay reported its extension never connected). Everything below was observed as a *guest*. Logged-in behaviour is taken from F95Checker source and marked `[source]`; it was not observed. See "Not verified".

Glossary used: Game, Source, Dev status, Game tag, Genre text, Update (see `CONTEXT.md`).

## 1. Endpoints found

| # | Endpoint | Auth | What it gives | Evidence |
|---|----------|------|---------------|----------|
| A | `GET https://f95zone.to/sam/checker.php?threads=<id,id,...>` | **none** | JSON `{"status":"ok","msg":{"<id>":"<version string>"}}`, max **100 ids** per call (101+ returns `{"status":"error","msg":"Invalid threads data or >100"}`) | Probed: 7 ids -> 146 B; all 180 CSV thread ids in 2 calls -> 178 answered. `indexer/f95zone.py:BULK_VERSION_CHECK_URL`; `indexer/watcher.py:poll_versions` uses it with no cookie |
| B | `GET /sam/latest_alpha/latest_data.php?cmd=list&cat=games&page=N&sort=date&rows=90&_=<unix>` | none (guest worked) | Site-wide "Latest Updates" feed as JSON, 90 rows/page, `pagination.total`=306 pages, `count`=27498 games | Probed; `indexer/watcher.py:poll_updates` sends the `xf_user` cookie, but a cookieless request returned 200 + full data today `[INFERENCE: may be tightened later]` |
| C | Thread HTML `GET /threads/<id>/` (301 -> slug URL, then 200) | none for most threads; **403 "Log in" page for restricted threads** (see 4) | Everything in the OP: title, prefixes, tags, body fields, ratings, images | Probed 7 threads |
| D | Forum RSS `GET /forums/games.2/index.rss` | none | Only the ~20 newest threads per forum, with title+OP excerpt. Per-thread RSS (`/threads/<id>/index.rss`) returns an error ("cannot be represented in this format") | Probed. Useless for a fixed ~190 thread watchlist |
| E | `https://api.f95checker.dev/fast?ids=...` (max 10 ids) and `/full/<id>?ts=<ts>` | none | Third-party cache: last-change timestamps; full parsed thread JSON (version, status, tags, downloads xpaths, ...) | Probed (`/fast?ids=67494,59416` -> `{"59416":1788025321,"67494":1790510853}`); `modules/api.py:api_fast_check_url`. Run by one volunteer; README says it is "an independent cache API ... specifically for this tool" |
| F | `/sam/dddl.php` (F95 donor DDL) and `/sam/latest_alpha/` page (embeds `var latestUpdates = {... "tags": {id: name}}`) | **login** | Tag id -> name map; DDL | `tags-diff.py` reads the tag map from the logged-in `/sam/latest_alpha/` page; guest fetch of that page returns 87 B (login notice). `[source]` |

No official F95zone API exists; F95Checker's README: "F95zone does not yet have a proper API serving the information needed by this tool".

## 2. Per data point

| Data point | Best source | Auth | Evidence / notes |
|------------|-------------|------|------------------|
| **Latest version** | A (bulk, cheapest). Fallback: thread title bracket / `Version:` body field | none | A returned `v4.34.1 Public` for 67494; identical to the title bracket `[v4.34.1 Public]` (thread HTML title). Body field says `4.34.1 Public` (no `v`), so A and body differ in the `v` prefix. B's `version` field also equals the title bracket (`"v0.9.166"` etc.). F95Checker prefers the A/B value over the body (`scraper.py`: "keep this version value") |
| **Dev status** | Thread prefix in C (`<h1 class="p-title-value">` labels); B has numeric `prefixes` ids | none | Prefix ids observed: 13 VN, 7 Ren'Py, 3 Unity, 18 Completed, 20 Onhold, 22 Abandoned (from thread label hrefs `?prefix_id=N`). Mapping: Completed->completed, Onhold->on hold, Abandoned->abandoned, none->ongoing (`parser.py` `game_has_prefixes`). Samples: 114650 `Completed`, 92250 `Abandoned`, 67426 `Onhold`, 59416 none. Not in A. B carries prefix ids only for games in Latest Updates `[INFERENCE]` |
| **F95 tag list** | Thread HTML `.js-tagList a.tagItem[href^="/tags/"]` (slug + display text) | none | 59416: 22 tags, 114650: 14, 254874: 9, 67426: 34 (`3dcg`, `ahegao`, ...). B has numeric tag ids (`"tags":[75,130,...]`); id->name map only on the logged-in page (F) so prefer thread HTML |
| **Genre text** | Thread OP body, `Genre:` field | **login required** | As guest, all 6 sampled threads render `Genre: [Spoiler] You don't have permission to view the spoiler content. Log in or register now.` (also Installation and Changelog spoilers). So Genre text is **not available to a guest** `[source for logged-in layout: parser.py get_game_attr/get_long_game_attr; not observed]` |
| **Last-updated date** | Thread OP `Thread Updated: YYYY-MM-DD` (and `Release Date:`) in body; B `ts` (unix, last promotion time, day-ish granularity in F95Checker via `datestamp`) | none | 67494: `Thread Updated: 2026-08-16`, `Release Date: 2026-08-15`; ld+json/`message-lastEdit data-time` exists (59416 lastEdit 1778952101). Free-text fields, developer-maintained: can be missing/typo. F95Checker falls back to `message-lastEdit` / post time and overrides with B `ts` when newer |
| **Download links** | Thread OP | **login required** | Guest sees `You must be registered to see the links` in place of every host link (67494). The mirror-name labels ("MEGA - GOFILE - ...") remain visible. Parsed by `get_game_downloads` `[source]`. Downstream: ticket on the downloader on `artemis` needs the user's `xf_user` cookie |
| **Cover image** | B `cover` (`https://preview.f95zone.to/YYYY/MM/<id>_<name>`; swap host `preview.` -> `attachments.` for full size, as `parser.attachment` does) or first `img.bbImage` in OP (`https://attachments.f95zone.to/YYYY/MM/thumb/...`, drop `/thumb/` for full size) | none | 59416 and 254874 OP first image is an `attachments.f95zone.to` thumb. `og:image` is unreliable (favicon for 4 of 6 samples; real `/data/covers/thread/o/59/59416.jpg` for one). Note: main-host `/attachments/` is `Disallow` in robots.txt; the `attachments.` and `preview.` hosts are different hosts (their robots.txt was not checked) |
| **Rating / votes** | Thread `application/ld+json` aggregateRating (59416: 4.4 / 75) or B `rating` | none | Out of scope but free |
| **Developer** | Title last bracket `[Raybae Games]`; B `creator` | none | |

## 3. Cloudflare / DDoS-Guard behaviour for a plain server-side client

- All responses came through Cloudflare (`server: cloudflare`, `cf-ray`). **No challenge page** for `curl` HTTP/2 with a custom UA and no cookies: thread pages 200 (~210-260 KB each, ~1 s), `/sam/*` JSON 200, RSS 200. Guest thread fetch sets only `xf_csrf` (name; not stored).
- Rate-limit signals documented in F95Checker source: HTTP 429, titles `429 Too Many Requests`, `Error 429`, `DDoS-Guard`; API-level JSON error `"You have been temporarily blocked because of a large amount of requests, please try again later"`; challenge markers `Just a moment...`, `_cf_chl_opt`; maintenance/backup-in-progress pages (`indexer/f95zone.py` RATELIMIT_*/TEMP_ERROR_MESSAGES). Not triggered in my ~25 requests.
- F95Checker's self-imposed limits: forum pages 1 req/2 s (`api.py` `f95_ratelimit_forum`), indexer 2 req/s, 10 retries with +5 s growing sleep after a rate limit.
- `robots.txt`: `User-agent: *` disallows `/account/ /attachments/ /goto/ /misc/language /misc/style /posts/ /login/ /search/ /whats-new/ /admin.php`; `Allow: /`. `/threads/`, `/sam/`, and `/forums/` are not disallowed. A long list of named AI and scraper agents (incl. `Scrapy`, `ClaudeBot`, `GPTBot`) is `Disallow: /`, and `DisallowAITraining`/`Content-Usage: ai=n` are set. **Do not use a UA such as Scrapy/python-requests-as-scrapy; use a descriptive own UA.** Daily personal tracking is not AI training.
- Thread `Cache-Control: private, no-cache`, `Last-Modified` = now, so no conditional GET is usable for thread HTML. Not tested for `/sam/*`: `checker.php` sends `no-store`.

## 4. Auth, cookies, restricted threads

- Cookie **names** F95zone sets (their `/help/cookies/`): `xf_csrf`, `xf_session`, `xf_user`, `xf__sam_ad_views`, `xf_xf_th_uix_*`. `xf_user` = "keeps you securely logged in between page visits".
- F95Checker's indexer logs in with **only `xf_user`** (`COOKIE_XF_USER`; comment "xf_user cookie should be enough for a long time"); with 2FA it additionally uses `xf_tfa_trust` (`tags-diff.py`). Logged-out detection: login-form HTML markers or `<pre>Sorry, you have to be logged in</pre>` on `/sam/latest_alpha/`.
- Cookie lifetime: not published by F95zone. XenForo's persistent `xf_user` normally lasts about a year `[INFERENCE: XenForo default, not observed here]`; read the real `Expires` from the user's browser once logged in.
- **2 of 180 CSV threads are 403 "Log in" for guests**: 94891 (Lycoris Radiata) and 151517 (Ovulating Maiden); both also absent from `checker.php`. Everything else (178) was readable as guest. Anything restricted needs the cookie.
- 4 CSV rows have no F95 thread id (3 itch.io/fanbox links and "Alchemy Shop"); they are out of scope for this ticket.

## 5. Version string: format and reliability

- Source of truth is the title's last-but-one bracket group: `Name [version] [developer]`; the thread `<title>` is `PREFIXES - Name [version] [developer] | F95zone ...`. Also exposed identically by `checker.php` and `latest_data.php`. The slug in the 301 redirect also embeds the version (`out-of-touch-v4-34-1-public-story-anon.67494`) but lossy (dots to dashes).
- Free-form, developer-chosen. In the CSV's 178 live values: most are `vX.Y.Z`; others seen live: `Ch.4 v1.0 Public`, `S2 R1`, `Ep.1-11 P1`, `Act 3 v0.97`, `Final`, `Day 3`, `Epilogue Full`, `Ch.end.07+p`, `R48`, date-style (`v2026-09-25 Full` in RSS). The CSV rows also mix `0.3a` / `v0.3a` styles, so the `v` prefix is not stable between sources (checker.php includes it, body field `Version:` does not).
- **Reliability for change detection**: compare the string for exact inequality only; do not parse as semver or order versions (non-numeric schemes above). Same-version threads still change (re-uploads, fixes); and the title can change without a new release (typo fixes, adding `Public`/`Full`), giving rare false positives. Real new releases that only edit the body (leaving the title) are false negatives; the body `Version:` and `Thread Updated:` fields are a second signal on detail fetch. F95Checker also treats a changed name or Dev status as an update trigger.
- A game in 'Latest Updates' vs 'unpromoted' updates: F95Checker README: "all version numbers tracked by F95zone Latest Updates are checked every 12 hours, this detects unpromoted updates", i.e. `checker.php` also reflects updates that never reach the Latest Updates feed.
- Drift example from the CSV: 67494 stored `v3.91.1`, live `v4.34.1 Public`; 114650 CSV `0.31.0`, live `v0.99.0` + `Completed` prefix: dev-status and version both drift.

## 6. Rules and terms constraints

- `/help/terms/` (read in full): no clause about bots, scraping, or automation; it is a content disclaimer, takedown, age/jurisdiction notice. Accounts may be suspended "at our discretion".
- The actual forum rules (`/threads/general-rules-updated-2018-september-29.5589/`) are **403 login-gated for guests**; I could not read them and no Wayback snapshot exists. Do not rely on web search results for "F95zone terms": the ones I found are for `f95zoned.com`, a different site.
- F95Checker has been tolerated for years with 1 req/2 s forum limits and the xf_user cookie; its maintainer routes bulk traffic via a cache to "put less stress on forum servers" (README). `[INFERENCE]` low request volume + own UA + no account-sharing is the lowest-risk posture. Using the user's own cookie from a home/NixOS server is account automation: unverified against the login-gated rules.

## 7. Request budget for ~190 threads (180 F95 ids)

- Daily check (strategy 1): `checker.php` x2 (<=100 ids each) = **2 requests/day**, no cookie. Compare returned strings to stored latest version. ~150 B per id.
- Optional second signal: one `latest_data.php?cat=games&sort=date&rows=90&page=1..3` (3 requests) shows which watched games were promoted in the last ~3 days (page 1 spanned 92 400 s = 25.7 h at 90 rows) and carries cover, creator, prefix ids, tag ids; adds little over `checker.php` for 180 ids. Skip unless needed.
- Detail fetch (first add, version change, weekly refresh): 1 thread HTML each (~230 KB). Daily steady-state: only changed threads (a few per day). Full initial import: 180 threads x 3 s sleep = 9 min, one time. Sleep >=3 s between thread requests.
- Restricted threads (guest 403, absent from checker.php): need the `xf_user` cookie for detail and cannot be bulk-version-checked; fetch thread HTML for these only (2 requests/day with cookie, or rely on user-triggered refresh).
- Do not depend on `api.f95checker.dev`: single volunteer service, 10 ids/call, cached up to 7 days, breaks the "daily" guarantee and leaks the watchlist.

## Not verified (needs a logged-in browser)

1. Genre text as it appears to a logged-in user (spoiler rendered?), and whether Genre can appear outside the spoiler. Need: 3-5 threads' OP HTML while logged in, e.g. `/threads/67494/`, `/threads/59416/`.
2. Download link markup (host names, `/goto/` redirect wrapper) when logged in. Need: same thread pages.
3. The two restricted threads (94891, 151517): why restricted, and whether Dev status/version appear when logged in.
4. `xf_user` real `Expires`/Max-Age and whether sessions are IP-bound. Need: browser cookie panel on f95zone.to.
5. The actual forum rules thread text (login-gated) for any automation clause.
6. Whether `checker.php` / `latest_data.php` keep working without cookie over weeks (guest fine today).

## Recommendation

- **Daily check: `GET /sam/checker.php?threads=<100 ids>` twice (no cookie, no login)**; keep the returned string per Game; any exact change vs. the stored latest version is an Update. 2 requests/day for 180 threads, with descriptive User-Agent, 3+ s spacing, and backoff on 429/`DDoS-Guard`/"temporarily blocked" responses. Treat a thread id missing from the response as "restricted or gone": detect it, do not alert.
- **Full detail fetch: thread HTML `GET /threads/<id>/`** (follow the 301), only on first add, on a detected Update, on user request, and a weekly sweep for Dev status/tag drift. Parse: prefixes (Dev status, ids 18/20/22), `.js-tagList` (F95 tag list), title bracket, `Thread Updated`, ld+json rating, first `img.bbImage` for cover (strip `/thumb/`). Expect ~230 KB/page, ~1 s.
- **A logged-in `xf_user` cookie is required only for**: Genre text, download links (the later `artemis` downloader), and the 2 restricted threads. Store it as a secret (env/file via the NixOS module; name `xf_user`, plus `xf_tfa_trust` if 2FA), send it only to `f95zone.to` thread fetches, and make the daily check cookie-free so a lapsed cookie never blocks Update alerts. Genre-dependent Game tags stay `unverified` for threads fetched without cookie.
- Not RSS (20 newest per forum only, no per-thread feed); not `api.f95checker.dev` (third-party dependency); `latest_data.php` is optional extra for covers/promotion times.
- Version strings: compare exact strings, never order or parse; show the raw string; accept occasional false positives/negatives.
- Terms: no automation clause on public `/help/terms/`, `robots.txt` allows `/threads/` and `/sam/`; the login-gated forum rules remain unread, so the cookie-authenticated part carries residual account-risk (see "Not verified" 5).
