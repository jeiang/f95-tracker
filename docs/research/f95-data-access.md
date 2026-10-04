# F95zone data access and update detection (ticket #2)

Method: live probes from this machine on 2026-10-04 with plain `curl` (custom UA `f95-tracker-research`, no cookies, >=3 s between F95 requests, ~25 F95 requests total), plus reading the source of WillyJL/F95Checker (shallow clone, `modules/api.py`, `indexer/*.py`, `common/parser.py`, `README.md`). Terms: `/help/terms/`, `/help/cookies/`, `/robots.txt`.
Raw samples: `docs/research/f95-data-access/`.

**Sections 1-7 were observed as a *guest*** (the logged-in relay was unavailable then); logged-in behaviour is now in section 8 (addendum). Where section 2/4/6 text conflicts with section 8 (restricted threads, cookies, rules), section 8 wins.

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
| **Genre text** | Thread OP body, `Genre:` field | **login required** | As guest, all 6 sampled threads render `Genre: [Spoiler] You don't have permission to view the spoiler content. Log in or register now.`. **Logged in (section 8.1): renders** in `<b>Genre</b>` + `div.bbCodeSpoiler` as comma-separated text on 12760, 31912, 98051 |
| **Last-updated date** | Thread OP `Thread Updated: YYYY-MM-DD` (and `Release Date:`) in body; B `ts` (unix, last promotion time, day-ish granularity in F95Checker via `datestamp`) | none | 67494: `Thread Updated: 2026-08-16`, `Release Date: 2026-08-15`; ld+json/`message-lastEdit data-time` exists (59416 lastEdit 1778952101). Free-text fields, developer-maintained: can be missing/typo. F95Checker falls back to `message-lastEdit` / post time and overrides with B `ts` when newer |
| **Download links** | Thread OP | **login required** | Guest sees `You must be registered to see the links` in place of every host link (67494). Logged-in markup, hosts, masked vs direct: section 8.2. Downstream: ticket on the downloader on `artemis` needs the user's `xf_user` + `xf_tfa_trust` cookies (8.4) |
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

## 8. Logged-in addendum (observed 2026-10-04 in the user's signed-in browser)

Requests: 6 thread page loads from my own tab (12760, 31912, 98051, 94891, 151517, rules thread 5589), 2 in-page `fetch` calls (`checker.php`, 403 re-check), 4 in-memory Bun `fetch` calls for the cookie test (section 8.4): 12 F95 requests, >=5 s apart.

Session was confirmed logged in (`<html data-logged-in="true">`, user nav present). Read-only; nothing clicked; no masked link followed.

### 8.1 Genre text (threads 12760, 31912, 98051)
- **Renders when logged in** on all 3 (guest: "You don't have permission to view the spoiler content").
- DOM, identical on all 3: the first post `article.message--post .bbWrapper` has, as **direct children**, `<b>Genre</b>` + text node `:` + `<br>` + `div.bbCodeSpoiler`; the value is the text of `div.bbCodeSpoiler > div.bbCodeSpoiler-content > div.bbCodeBlock.bbCodeBlock--spoiler > div.bbCodeBlock-content`. The spoiler button has no useful title (`Spoiler`), so locate by label: the `<b>` whose text starts with `Genre`, then the next sibling element with class `bbCodeSpoiler`. The content is present in the served HTML (no click or XHR needed).
- Values (truncated): 12760 `3DCG, Male protagonist, Animated, Incest, Harem, Dating sim, Trainer, Lesbian, ...`; 31912 `3DCG, Adventure, Ahegao, Animated, Corruption, Fantasy, Male protagonist, Masturbation, ...`; 98051 `3DCG, Animated, Male Protagonist, Mind Control, Anal Sex, Creampie, ...`. Comma-separated; casing and spelling are developer-chosen and differ across threads (`Male protagonist` vs `Male Protagonist`; 12760 contains the typo `Exhibtionism`). Normalize case-insensitively when matching Game tags; do not trust spelling.
- Genre text never appeared outside the spoiler in these 3 threads (the `b` search found only the spoiler form). Other threads may differ `[INFERENCE]`.

### 8.2 Download block (same 3 threads)
- Marker: `<b><span style="font-size: 22px">DOWNLOAD</span>[<br><span style="font-size: 18px">Win/Linux | Part 2 ...</span>]</b>` (not an id/class; locate by text `DOWNLOAD`). It is a direct child run of `.bbWrapper`, **not** inside a spoiler. Everything after it up to the end of the post is the block, free-form per developer/uploader.
- Layout: plain text lines `Platform: HOST - HOST - HOST` (platform `Win/Linux`, `Mac`, `Android`, `Others`; 98051 and 12760 add `Part N` / `SPLIT` rows) where each `HOST` is an `<a class="link link--external has-favicon" target="_blank" rel="nofollow noopener" style="background-image:url(...favicon...)">HOSTNAME</a>`; the link text is the upper-case host label (`MEGA`, `PIXELDRAIN`, ...). There is no data-attribute and no per-link id. Older/alternate releases are nested in further `div.bbCodeSpoiler` blocks (labels such as `Spoiler` + "Before the Remake"), roughly half of all links sit in those spoilers. Each platform line repeats the same hosts, so 31912 has 45 links, 98051 20, 12760 40, but only ~8-11 distinct hosts.
- **Masked** (hosts that F95zone proxies): `PIXELDRAIN`, `MEGA`, `WORKUPLOAD`, `GOFILE`. Shape `https://f95zone.to/masked/<host-domain>/<threadId>/<n>/<tok27>/<tok22>/<tok43-107>` (host-domain e.g. `pixeldrain.com`, `mega.nz`, `workupload.com`, `gofile.io`; `<threadId>` = this thread; `<n>` a numeric id; three opaque tokens). Resolving needs a `GET` to the masked URL (interstitial/redirect); not followed here (disallowed by the task). `/goto/` is `Disallow` in robots.txt; `/masked/` is not listed there.
- **Direct** (href is the host URL): `BUZZHEAVIER` -> `https://bzzhr.to/<id>`; `DATANODES` -> `https://datanodes.to/<id>/<file>`; `VIKINGFILE` -> `https://vikingfile.com/f/<id>`; `MIXDROP` -> `https://mixdrop.ag/f/<id>`; `KRAKENFILES` -> `https://krakenfiles.com/<seg>/<id>/<seg>` (3 path segments; literal segment names not recorded); `UP2SHARE` -> `https://up2sha.re/<id>`; `BUNKR` -> `https://bunkr.pk/f/<id>`; `UPLOADHAVEN` -> `https://uploadhaven.com/<seg>/<id>` (2 path segments). Anomaly: 12760 `BOWFILE` links have href host `cancerads` (`https://cancerads/<id>`, no TLD), i.e. not a resolvable URL; treat unknown/TLD-less hosts as unusable.
- Hosts seen per thread: 12760 bowfile (broken href), buzzheavier, datanodes, vikingfile, mixdrop, pixeldrain; 31912 buzzheavier, datanodes, krakenfiles, pixeldrain, vikingfile, mega, mixdrop, up2share, workupload, bunkr, gofile; 98051 buzzheavier, datanodes, vikingfile, pixeldrain, mega, gofile, mixdrop, uploadhaven.
- Parsing consequence: label = link text, platform = nearest preceding `Platform:` text, mask status = `href` starts with `https://f95zone.to/masked/`. Extras (`Fan Sigs`, `Wiki`, Patreon/Discord/Itch/SubscribeStar developer links) are also `a.link--external` in the same area: filter by host list or by text.

### 8.3 Restricted threads 94891 and 151517
- **Still not readable when logged in** (logged-in confirmed on the same page). `GET /threads/94891/` and `/threads/151517/` render the title `Oops! We ran into some problems.` with the message **`You do not have permission to view this page or perform this action.`** (HTTP 403 confirmed for 94891 via `fetch`; URL is not redirected to a slug). No title, prefix, version or Genre is available. Both ids are also absent from `checker.php?threads=94891,151517,12760` while logged in (response `{"status":"ok","msg":{"12760":"v0.20 pre alpha"}}`).
- The message is the generic XenForo no-permission page; it does not state why. `[INFERENCE]` the threads were removed/hidden by staff (the rules say banned-content threads are removed "without warning" and "may be restored at a later date", see 8.5); this was not verified. The tracker must treat them as `unavailable`.

### 8.4 Cookies (from `tab.cookies()`, names/flags/expiry only; values never printed or stored)
| Name | Domain | httpOnly | Secure | SameSite | Expires |
|---|---|---|---|---|---|
| `xf_user` | `f95zone.to` | yes | yes | Lax | **2027-09-25** (355 days from 2026-10-04; `[INFERENCE]` issued ~2026-09-25 with 1-year lifetime) |
| `xf_session` | `f95zone.to` | yes | yes | Lax | session |
| `xf_csrf` | `f95zone.to` | no | yes | Lax | session |
| `xf_tfa_trust` | `f95zone.to` | yes | yes | Lax | **2026-10-26** (22 days) |
| `cf_clearance` | `.f95zone.to` | yes | yes | None | 2027-10-04 (Cloudflare, 365 days) |

- The account has two-factor auth (the `xf_tfa_trust` cookie exists).
- **Server-side test** (in-memory use of the browser's cookies from a plain Bun `fetch`, own UA `f95-tracker-research`, no `cf_clearance`, thread 12760; nothing stored or printed): 
  - `xf_user` alone: **HTTP 414 "Request-URI Too Large" from nginx** (1108 B), not a login; unusable.
  - `xf_user` + `xf_tfa_trust`: **200**, `data-logged-in="true"`, Genre spoiler content and `/masked/` links present; response sets new `xf_session` and `xf_csrf`.
  - `xf_user` + `xf_session`: **200**, same logged-in content.
  - Pages are ~748 KB when logged in (vs ~230 KB as guest). No Cloudflare challenge without `cf_clearance`.
- Conclusion: a server-side client needs **`xf_user` plus either `xf_tfa_trust` (2FA accounts) or a live `xf_session`**, and should keep a cookie jar so the server's `Set-Cookie` of `xf_session`/`xf_csrf` is reused. `xf_csrf` is only needed for POSTs (this tracker never posts). Lifetimes: `xf_user` ~1 year (renew by logging in again; the user must copy fresh cookie values by hand), `xf_tfa_trust` expires 2026-10-26 `[INFERENCE: xf_user + live xf_session also worked, so a jar that keeps xf_session alive may outlive xf_tfa_trust; not tested]`. Whether `xf_session` expiry or IP changes invalidate a session was not tested. F95Checker's claim "xf_user is enough" did not hold in the above test with a non-browser client.

### 8.5 Forum rules (logged in)
- Read `https://f95zone.to/threads/general-rules-updated-2026-08-05.5589/` ("README - General Rules [Updated 2026-08-05]", 177 lines, 10 sections). **No rule on bots, scrapers, crawlers, scripts, automation, or API use** (keyword search for bot/scrap/crawl/automat/script/api found only: "Do not mass-report users or threads to continue an argument, target someone you dislike, or force moderator attention.").
- Relevant adjacent text: "Alt accounts are allowed, so long as they're not used to break any of the above rules, circumvent bans, ..."; "Do not abuse the Report or Ticket systems."; "If you are unsure whether something is allowed, ask Staff before posting."; section 10: "Thread-specific Moderation Notices carry the same weight as the rules listed here." The "Game Uploading Rules" and per-forum notices were not read; they could contain more.
- Combined with `/help/terms/` (no clause) and `robots.txt` (`/threads/`, `/sam/` allowed; `/goto/`, `/attachments/`, `/account/` disallowed): no written ban on a personal low-rate client was found; account-suspension discretion remains.

## Not verified
1. Whether `xf_session` or IP change invalidates a server-side session; actual renewal behaviour of `xf_user`/`xf_tfa_trust` after 2026-10-26.
2. Why 94891 and 151517 are restricted (generic no-permission message only).
3. The masked-link resolution path (not followed by design), and whether `/masked/` fetches count as automation.
4. Game Uploading Rules and other sub-forum notices (not read) for automation clauses.
5. Whether `checker.php` / `latest_data.php` keep working without cookie over weeks (guest fine today).
6. Whether Genre text ever appears outside the spoiler, or in other layouts, across all 178 threads (3 sampled).

## Recommendation

- **Daily check: `GET /sam/checker.php?threads=<100 ids>` twice (no cookie, no login)**; keep the returned string per Game; any exact change vs. the stored latest version is an Update. 2 requests/day for 180 threads, with descriptive User-Agent, 3+ s spacing, and backoff on 429/`DDoS-Guard`/"temporarily blocked" responses. Treat a thread id missing from the response as "restricted or gone": detect it, do not alert. 94891 and 151517 are in that set even for the signed-in user, so mark them `unavailable`, not an auth problem.
- **Full detail fetch: thread HTML `GET /threads/<id>/`** (follow the 301), only on first add, on a detected Update, on user request, and a weekly sweep for Dev status/tag drift. Parse: prefixes (Dev status, ids 18/20/22), `.js-tagList` (F95 tag list), title bracket, `Thread Updated`, ld+json rating, first `img.bbImage` for cover (strip `/thumb/`), and (logged in) Genre text via the `<b>Genre</b>` label followed by `div.bbCodeSpoiler` (section 8.1). Expect ~230 KB guest / ~750 KB logged-in, ~1 s.
- **Cookies are required only for**: Genre text, download links (the later `artemis` downloader), nothing else (the 2 restricted threads are not readable even with them). Send **`xf_user` + `xf_tfa_trust` (this account has 2FA)** and persist the `xf_session`/`xf_csrf` the server returns in a jar; `xf_user` alone returns 414 (section 8.4). Store as secrets (env/file via the NixOS module), send only to `f95zone.to` thread fetches, keep the daily check cookie-free, and surface a visible "refresh cookies" state: `xf_tfa_trust` expires 2026-10-26, `xf_user` 2027-09-25. Genre-dependent Game tags stay `unverified` for threads fetched without cookie.
- Downloads: only 4 hosts are masked (`pixeldrain`, `mega`, `workupload`, `gofile`); the rest are direct. The downloader must resolve `/masked/` URLs through F95zone (not tested) or prefer direct hosts (`bzzhr.to`, `datanodes.to`, `vikingfile.com`, `mixdrop.ag`, ...).
- Not RSS (20 newest per forum only, no per-thread feed); not `api.f95checker.dev` (third-party dependency); `latest_data.php` is optional extra for covers/promotion times.
- Version strings: compare exact strings, never order or parse; show the raw string; accept occasional false positives/negatives.
- Terms: no automation clause found on public `/help/terms/`, in the logged-in General Rules (section 8.5), or in `robots.txt`; the Game Uploading Rules remain unread, so residual account risk of using the user's cookie remains low-evidence rather than zero.
