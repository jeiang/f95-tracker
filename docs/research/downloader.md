# Research: downloader feasibility (ticket #6)

Question: what can an unattended downloader on `artemis` do with F95zone download links, and what are the hard limits?

Evidence legend: **[src]** = read in source code/official docs (URL given); **[obs]** = observed by me this run with curl/read; **[INFERENCE]** = not observed.

## Method and limits of this run

- The logged-in browser relay was **not available** (omp relay extension never connected). I did not load any F95zone page while logged in and did not click any link.
- A logged-out public read of thread 134987 shows every link block as "You must be registered to see the links" **[obs]**: `https://f95zone.to/threads/134987/`. So **all download links need an F95zone login** (hard limit #1).
- Substitute for the logged-in view: F95Checker's public indexer cache (`https://api.f95checker.dev/full/<id>?ts=0`, code in `indexer/threads.py`), which publishes each thread's parsed download table. Masked links are kept as `https://f95zone.to/masked/<host>/<token>`; all other external links are replaced by an XPath placeholder, but the **label** (host name shown on the thread) survives **[src]** `common/parser.py` lines 135-192 (F95Checker repo). I fetched 7 threads from the CSV (134987, 67494, 31912, 98051, 125074, 92250, 254874), 3 s apart. Sanitised structure (no link tokens): `downloader/host-listing-sample.json`.
- Consequence: the host *labels* are what the thread shows, but whether a non-masked host's real href is a plain direct link is **[INFERENCE]** from the parser: non-`f95zone.to` hrefs are treated as plain links, only `f95zone.to/masked/...` hrefs are masked.

## 1. How F95zone download links work

1. **Login wall**: links are hidden from guests (above).
2. **Two link kinds in the post**:
   - *Masked*: `https://f95zone.to/masked/<host>/<token>` (F95Checker `modules/gui.py` ~L2693, `callbacks.redirect_masked_link`). In my 7-thread sample only four hosts were masked: **pixeldrain, mega, gofile, workupload**. All others (buzzheavier, datanodes, vikingfile, mixdrop, mediafire, uploadhaven, krakenfiles, up2share, bunkr, yourfilestore) appeared as plain external links **[obs from cache + src parser]**.
   - *Plain external*: the real host URL is in the post HTML; needs only the login to read.
3. **Unmasking a masked link** (two independent implementations agree):
   - Userscript `F95Zone masked link unlocker` (https://gist.github.com/djluiso/2f3fd871926b5f0da0cd8f1809c0102f): `POST <masked url>` with body `xhr=1&download=1&captcha=<g-recaptcha token>`; response JSON `{status:"ok", msg:"<real url>"}`. Uses Google **reCAPTCHA v2** (explicit render, public sitekey in the script). It caches one solved token in a cookie for ~2 min and replays it for **every** masked link on the page.
   - F95-Manager (https://github.com/farvend/F95-Manager, `src/parser/game_info/link.rs`): same POST with the user's F95 session cookies and body `xhr=1&download=1`; if the response `status == "captcha"` it fails with `DownloadError::Captcha` and prompts the user to pass the captcha. So when F95 has recently seen a solve for the session, no new captcha is requested; otherwise a captcha is mandatory.
   - F95Checker (https://github.com/WillyJL/F95Checker `modules/callbacks.py::redirect_masked_link`, `modules/webview.py::css_redirect`): opens an embedded Qt WebEngine on the masked URL, auto-clicks `a.host_link`, and captures the first navigation to a different host. The **user solves the CAPTCHA by hand** inside that window. F95Checker's own docs describe masked links as "CAPTCHA protected link, unmasked with automated integrated browser".
4. **F95zone Donor DDL** (F95Checker `modules/api.py` `ddl_file_list` / `ddl_file_link`, endpoint `/sam/dddl.php`): F95-hosted direct downloads with SHA-1, no captcha, but only for **donors** ("Not a donor" error). Not available unless the user is a donor **[src]**.
5. **RPDL** (`modules/rpdl.py`, `dl.rpdl.net`): torrent mirror of many F95 games with its own account/API. F95Checker supports it. Out of scope for the F95 link path but a captcha-free fallback worth a later ticket **[src]**.
6. No known open-source tool fully removes the captcha. F95Checker, F95-Manager and the userscript all hand the captcha to a human. I did not look for, and do not recommend, captcha-solving services (ToS and account-ban risk to the single F95 account).

## 2. Host tally (7 threads from the CSV, via F95Checker cache)

Counting distinct threads that list the host (7 threads total) and total listings (one per platform/part/mirror line):

| Host | Threads (of 7) | Listings | Masked on F95? |
|---|---|---|---|
| pixeldrain | 7 | 37 | yes |
| mega | 7 | 35 | yes |
| buzzheavier (bzzhr.to) | 6 | 27 | no |
| datanodes | 6 | 16 | no |
| vikingfile | 5 | 23 | no |
| gofile | 4 | 8 | yes |
| mixdrop | 4 | 16 | no |
| mediafire | 2 | 6 | no |
| uploadhaven | 2 | 4 | no |
| workupload | 2 | 10 | yes |
| krakenfiles, up2share, bunkr, yourfilestore | 1 each | 3-4 | no |

Takeaways: **pixeldrain and mega were on every thread of the cache sample** (the logged-in addendum below found pixeldrain on 6/6 live threads and mega on 5/6, not 12760). Hosts vary per release/part, so the downloader must be "try mirrors in order", like F95-Manager does ("Mirrors are tried in order until one succeeds", F95-Manager README).

## 3. Per-host analysis

nixpkgs checked via the nixos MCP tool (unstable channel, this run).

| Host | Resolved link → file | Captcha / human step | Limits (free, anonymous) | API / CLI | In nixpkgs | Class |
|---|---|---|---|---|---|---|
| **pixeldrain** | `GET https://pixeldrain.com/api/file/{id}` with no key; range requests supported (https://pixeldrain.com/api) | Normally none; captcha required on rate-limited files (a file with 3x more downloads than views), on IP limit hits, server overload, malware flag; captcha only appears on the viewer page `/u/{id}` (API doc) | Free: download count, transfer and concurrent-connection limits per IP; `GET /api/misc/rate_limits` returned `download_limit 10000`, `transfer_limit 6000000000` (6 GB) for my IP **[obs]**; hotlink-detected 403 unless uploader/downloader is premium (API doc) | Official REST API, no key needed to download; gallery-dl has an extractor (https://github.com/mikf/gallery-dl supportedsites) | curl/aria2/gallery-dl/rclone yes | **Automatable**, with fallback to human step when a captcha error code (`*_captcha_required`) is returned |
| **mega** | public file/folder link with key in fragment | None for download; free quota counted per IP over 6 h, dynamic, resumes automatically; quota only applies after a file has been fully transferred >10 times (https://help.mega.io/plans-storage/space-storage/transfer-quota) | Dynamic IP-based quota; interrupted transfers discarded after 48 h | MEGAcmd `mega-get <link> <dir>` works without login (https://github.com/meganz/MEGAcmd/blob/master/UserGuide.md); megatools: maintained, but mega.nz returns 402 blocks to megatools at times, hashcash handling added 2025-07 (https://xff.cz/megatools/) | `megacmd` 2.5.2, `megatools` 1.11.5 | **Automatable** (quota waits, stop/resume are normal). MEGAcmd is the first-party tool; needs its background server running |
| **gofile** | folder id → `GET https://api.gofile.io/contents/<id>` with `Authorization: Bearer <guest token>` + `X-Website-Token` header; file `link` then downloaded with the `accountToken` cookie | None observed | Guest account is created via `POST /accounts`. The website token is a SHA-256 over UA, language, account token, a 4 h bucket and a **hardcoded secret**; gallery-dl and F95-Manager use *different* secrets, so it rotates (https://github.com/mikf/gallery-dl gofile.py; F95-Manager `link/gofile.rs`). Official Bearer API is documented but the website token is not | gallery-dl extractor maintained; no official CLI | gallery-dl 1.32.14 | **Automatable but brittle**: depends on a reverse-engineered token. Use gallery-dl (tracks changes) rather than own code; a gofile premium API token is configurable (`api-token`) |
| **workupload** | page with short code | Image/text captcha code present in the site's JS (`captcha` fields seen on `workupload.com/start` **[obs, not exercised on a file page]**) | unknown | none official; F95-Manager treats it as unsupported ("general", not in downloadable subset) | none | **Human step / not automatable** |
| **buzzheavier** (bzzhr.to) | file page → redirect | `https://buzzheavier.com/` returns a **Cloudflare managed challenge** to curl (HTTP 403, `cf-mitigated: challenge`) **[obs]**; whether file pages are challenged too is **[INFERENCE]** | unknown | no documented public API found; none in nixpkgs | none | **Human step** (needs a real browser); treat as not automatable on headless artemis |
| **datanodes** | file page | reCAPTCHA string present on site root **[obs]**; file-page flow unverified | unknown | none known | none | **Human step [INFERENCE]** |
| **vikingfile** | file page | none seen on root **[obs]**; file-page flow unverified | unknown | site references an API (upload side) **[obs]**, download API not verified | none | **Unverified**; assume human step |
| **mixdrop** | file page → embed | none seen on root; gallery-dl has an extractor, which implies scrapeable (https://github.com/mikf/gallery-dl supportedsites) | ad/overlay flow | gallery-dl | gallery-dl | **Automatable via gallery-dl, unverified**, low priority |
| **mediafire** | file page → scrape direct link | reCAPTCHA string on site root **[obs]**; direct-link scraping is commonly done but I did not verify it | per-IP | no official download CLI; no nixpkgs package named mediafire | none | **Unverified / not recommended** |
| **bunkr** | album/file page | none seen on root; gallery-dl extractor exists | domain churn | gallery-dl | gallery-dl | **Automatable via gallery-dl, unverified** |
| uploadhaven, krakenfiles, up2share, yourfilestore | file page | recaptcha string on up2share and yourfilestore root **[obs]**; others unverified | unknown | none | none | **Human step / unsupported** |

Generic tools available in nixpkgs: `curl` 8.22, `wget`, `aria2` 1.37.0, `rclone` 1.75.1 (has Mega, Gofile and Pixeldrain backends; account-oriented, not verified for anonymous public links), `yt-dlp`, `gallery-dl` 1.32.14, `megacmd`, `megatools`, `plowshare` 2.1.7 (nixpkgs has it; I did not verify it supports any of these hosts and its upstream is old), `playwright-driver`, `chromium`. **Not** in nixpkgs: JDownloader, pyLoad, cyberdrop-dl.

## 4. Classification

- **Fully automatable once a real URL is known**: pixeldrain (API), mega (MEGAcmd), gofile (gallery-dl; brittle token).
- **Automatable only after a human solves F95's masked-link captcha**: any link served as `f95zone.to/masked/...` (today pixeldrain, mega, gofile, workupload). The captcha gate sits on **F95zone**, not on the host: the downloader cannot start from the thread without a human step unless F95 happens to have a recent solve for that session.
- **Needs a human step or a real browser**: workupload, buzzheavier (Cloudflare challenge), datanodes, mediafire and likely the other plain-link hosts with reCAPTCHA.
- **Not automatable**: none proven impossible, but captcha-gated hosts are out of scope by constraint (no solver).

Key point for the architecture: because **pixeldrain and mega appeared on nearly every sampled thread** (all but mega on 12760) and both are automatable *after* unmasking, a human captcha solve on F95 plus MEGAcmd/pixeldrain API covers most sampled games without touching the harder hosts. Exception: 12760's headline archive has neither (see addendum).

## 5. How the user's logged-in browser can cover the human step

Precedents: F95Checker ships Chrome/Firefox extensions (`browser/chrome`, `browser/firefox`) that talk to the desktop app over a local HTTP RPC (`modules/rpc_thread.py`, `127.0.0.1:<rpc_port>` with an origin allow-list). F95Checker also keeps the user's F95 login in its own webview.

Options for this tracker (server on a NixOS host, downloader on `artemis`, user's browser on any device):

1. **Extension/userscript that resolves and pushes**: on an F95 thread page the user is already logged in; the script solves nothing itself, but after the user ticks the one reCAPTCHA (as the djluiso userscript does), it POSTs each resolved real URL (and the user's chosen game id/version) to the tracker's API (authenticated via the tracker's OIDC session or an API token). The tracker queues a **download job** with the real URL for artemis. Pro: captcha solved once per page for all links (token replay per the userscript) and **F95 cookies never leave the browser** (no account risk from server-side requests). Con: a browser extension to maintain.
2. **Hand-off page in the tracker**: the tracker shows a job with the thread link and a "paste resolved link" box. No extension, but the user must reveal each link by hand. Good as the zero-dependency fallback.
3. **Server-side unmask with the user's F95 cookie** (what F95-Manager does): needs the F95 session cookie stored on the server/artemis and still hits the captcha. Rejected: stores a credential, risks the account, and still needs human captcha.
4. **Donor DDL** if the user is a donor: fully automatable (no captcha), but depends on donor status.

## 6. Constraints for the later protocol grilling

1. The downloader consumes **resolved real URLs** (host + URL + optional filename/size/checksum), never masked links and never F95 credentials. Unmasking is a human-in-browser step.
2. A job is `(game, version, ordered mirror list, chosen file)`; the downloader tries mirrors in order and reports per-mirror failure reason (captcha-required, quota, 403/Cloudflare, not found). Failures that need a human go back to the tracker as "needs human" instead of retrying forever.
3. Host adapters are pluggable and small; v1 adapters: pixeldrain (REST), mega (MEGAcmd `mega-get`), gofile (gallery-dl). Everything else → "needs human / manual download".
4. Resolved links are short-lived (masked token + captcha token ~2 min; gofile/pixeldrain direct links expire) so jobs must be dispatched promptly; the protocol needs an expiry timestamp.
5. artemis is a home desktop that may be off: the downloader **pulls** jobs from the tracker (outbound only, no inbound port to artemis), polls or long-polls, reports progress/state, and is idempotent/resumable (pixeldrain supports range requests; MEGAcmd resumes; free quotas are per IP per 6 h).
6. Respect host limits: concurrency 1 per host, honour pixeldrain/mega quota errors by backing off rather than rotating anything.
7. Post-download: verify size/checksum when provided, extract, then record in the tracker; the tracker never needs the game files.
8. Authentication between tracker and downloader and between extension and tracker is an open design item for grilling (OIDC session vs. per-device API token).
9. Open verification (see "Logged-in addendum" and "Still unverified"): masked-page captcha scope, real bowfile href, and file-page behaviour of non-masked hosts. Live hosts per thread are now observed.

## Logged-in addendum (6 live threads, 8 page loads, nothing clicked)

Method: the user's signed-in browser (own tab, CDP), first post of 12760, 31912, 98051, 134987, 50488, 125074 (12760 and 31912 loaded twice). I only read DOM `href`s and scripts; I did not click, did not load any `/masked/` URL, and kept no tokens. **[obs]** unless marked.

### A. Hosts per platform (headline block = the visible, un-spoilered "DOWNLOAD" lines for the current version)

Each cell lists host (M = masked `f95zone.to/masked/...`, D = direct external href).

| Thread | Win/Linux | Mac | Android |
|---|---|---|---|
| 12760 | bowfile, buzzheavier, datanodes, vikingfile (all D; 0 M) | same 4 (D) | none |
| 31912 | buzzheavier, datanodes, krakenfiles, vikingfile (D); pixeldrain (M) | same 5 | same 5 |
| 98051 | buzzheavier, datanodes, vikingfile (D); pixeldrain, mega (M) | same 5 | none |
| 134987 | (Windows and Linux each) mediafire, buzzheavier, datanodes (D); pixeldrain, mega (M) | same 5 | same 5 |
| 50488 | (Win/Linux/Mac one line) buzzheavier, datanodes, vikingfile (D); mega, pixeldrain (M) | in same line | separate `Android (v1.60)` line, same 5 hosts |
| 125074 | (Win only) buzzheavier, datanodes, vikingfile (D); pixeldrain, mega (M) | none | none |

Totals over those headline lines: 14 platform lines; 9 of them carry 2 masked links, 3 carry 1, 2 carry none (12760). Masked links in headline lines are **only pixeldrain and mega**. Everything else is direct.

Spoiler blocks ("older/other mirror sets" and split `Part N` lines) add more: 31912 has 24 masked links in total (pixeldrain 9, mega 6, workupload 6, gofile 3); gofile and workupload appear **only** inside spoilers (31912, 98051, 50488). Platform lines repeat the same host list for Win/Linux, Mac and Android, so per platform the host set is the same; links differ per platform and per part.

Across all six posts (incl. spoilers) masked hosts: pixeldrain 6/6, mega 5/6 (not 12760), gofile 3/6, workupload 3/6. No pixeldrain, mega or gofile link was direct in any of the six posts.

### B. URL shapes (lengths for random parts, no values)

Masked: `https://f95zone.to/masked/<host-domain>/<thread id>/<7-digit number>/<27>/<22>/<64|107|43>` (token length varies with host: pixeldrain/workupload 64, mega 107, gofile 43). In 31912 all 24 hrefs are unique (per-link tokens). The 7-digit number is identical for every link in the post (and, **[INFERENCE]**, matched on both 31912 and 12760, so it is likely the viewer's id rather than a post id; not recorded). `<a target=_blank rel="nofollow noopener" class="link link--external has-favicon">`; no `data-*` attributes, no inline handler.

Direct hosts, as the thread links them:

| Host | Shape |
|---|---|
| buzzheavier | `https://bzzhr.to/<12 chars>` |
| datanodes | `https://datanodes.to/<12 chars>/<filename>` (filename carries the release name) |
| vikingfile | `https://vikingfile.com/f/<10 chars>` |
| mixdrop | `https://mixdrop.ag/f/<14-15 chars>` |
| krakenfiles | `https://krakenfiles.com/view/<10 chars>/file.html` |
| mediafire | `https://www.mediafire.com/file/<id>/<name>/file` (134987) or `/file/<15 chars>` (50488) |
| bunkr | `https://bunkr.pk/f/<13 chars>` |
| up2share | `https://up2sha.re/file?f=<...>` |
| uploadhaven | `https://uploadhaven.com/download/<32 chars>` form |
| files.fm, we.tl | `https://files.fm/u/<9>`, `https://we.tl/<12>` |
| bowfile | href host is the literal `cancerads` (`https://cancerads/<5>`), not a resolvable domain. **[INFERENCE]** a placeholder or something rewriting the link in the user's browser (extension/ad blocker); the real bowfile URL was not observed. Treat bowfile as unusable until checked in a clean profile |

Because no pixeldrain/mega/gofile href is direct, the planned adapters receive these only after unmasking, in the host forms they already parse (`pixeldrain.com/u/<id>`, `mega.nz/file/<id>#<key>` or `/folder/...`, `gofile.io/d/<id>`) **[INFERENCE: the post-unmask target was not observed]**.

### C. How masked links get resolved (markup and scripts only)

- Thread-page behaviour **[obs]**: masked links are ordinary `<a href="https://f95zone.to/masked/...">` that open a new tab. None of F95zone's loaded scripts (`xf/*`, `themehouse`, `siropu`, `sv`, searched for `masked|recaptcha|hcaptcha|turnstile`) handles them, and the thread page has no captcha widget, `.g-recaptcha`, `[data-sitekey]`, captcha iframe or `grecaptcha` global. The only inline script that mentions `masked` just extracts the host from the path to pick the favicon.
- So resolution happens on the **masked-link page itself** (a separate page load), not by an XHR from the thread page. The endpoint is the masked URL itself; `POST <masked url>` with `xhr=1&download=1[&captcha=...]` is known from the userscript and F95-Manager (section 1) **[src; not re-observed: I did not load a masked page]**.
- Captcha scope **[INFERENCE]**: the widget is rendered on the masked page, so it is per link page. Whether a solve unlocks the others is not visible from the thread page (no hint text; searching the page text for "captcha" returned nothing). The "one solve covers all" claim still rests only on the userscript's cookie-replay design and F95-Manager's `status:"captcha"` branch (section 1).

## Still unverified

- The masked page itself: widget type, whether one solve carries to other links, post-unmask target forms. Needs one deliberate masked-page load (reveals one link) which was out of bounds here.
- The real bowfile href (`cancerads` host).
- File-page behaviour of the non-masked hosts beyond landing pages (unchanged from above).

## Recommendation

Do not try to automate the F95zone captcha. Make the downloader a **pull-based worker on artemis that only accepts already-resolved host URLs**. The user clears the one human step, F95's reCAPTCHA on masked links, in their own logged-in browser. The recommended way is a small userscript/extension that unmasks every masked link on the thread after a single solve and posts the real URLs to the tracker. The fallback is a "paste resolved link" hand-off page in the tracker. Never store the F95 cookie on the server or artemis.

Host support for the first version: **pixeldrain** (official REST API, no key), **mega** (MEGAcmd `mega-get`, no login, IP-based quota that resumes), and **gofile** (via gallery-dl; reverse-engineered token, so mark brittle). **Logged-in addendum correction**: pixeldrain was on 6 of 6 live threads and mega on 5 of 6 (not 12760), but all of them are *masked* everywhere, so the three planned adapters never receive a link straight from the thread; they only ever get URLs the user's browser unmasked. Adapters therefore take a resolved URL, never a thread link. On thread 12760 the headline (full-archive) block has no masked or automatable host at all (bowfile, buzzheavier, datanodes, vikingfile only); pixeldrain appears only as split `Part N` links. The downloader must therefore cope with "no supported mirror: needs manual download" even for pixeldrain-era games, and with multi-part archives. **workupload, buzzheavier (Cloudflare challenge), datanodes, mediafire and the remaining hosts need a human step** and should be surfaced as "download manually" with the mirror list rather than attempted. Treat mixdrop and bunkr (gallery-dl extractors exist) as optional later adapters. If the user is an F95 donor, Donor DDL (`/sam/dddl.php`) is a captcha-free alternative worth a separate check.
