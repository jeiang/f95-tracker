# Research: itch.io update detection (ticket #7)

Question: how can the tracker detect new versions of itch.io Games cheaply and reliably?

All probes were run unauthenticated with `curl` on 2026-10-04 (server `Date` header), at most one request every 1.5 to 3 s. No credentials were used or stored. "Live samples" are the three itch.io rows of the CSV:

| Sample | Slug | Game id (from `data.json`) | Notable property |
|---|---|---|---|
| S1 | `kuro-kai/lycoris-radiata` | 1547278 | Devlog posts, version only in upload names ("Ch7") |
| S2 | `abbys-cat/my-new-second-chance` | 2525820 | Version in page title (`[EP33]`), devlog, uploads with "Version N" |
| S3 | `jjambong/alchemy-shop` | 1403212 | Browser (HTML5) game, no devlog, no uploads, no "Updated" row |

## Findings per method

### 1. Devlog RSS: `<game-url>/devlog.rss`
- Discovery: each game page carries `<link href=".../devlog.rss" rel="alternate">` (seen in S3's HTML even though S3 has no devlog).
- S1: 200, `content-type: application/xml`, RSS 2.0, items have `guid`, `title`, `link`, `pubDate` (RFC 822, GMT), `category`=`devlog`, HTML `description` in CDATA. Newest item "Progress Update #6", `pubDate` Sun, 20 Sep 2026 10:49:12 GMT.
- S2: 200, same shape. Newest item "The EP33 has just been released!" 18 Sep 2026 23:37:39 GMT; earlier "The EP32 has just been released!" (3 Sep). Titles of release posts carry the version, but only because this developer names them so. S2 also has non-release posts ("Devlog #55 ...").
- S3: **404** (`text/html` itch.io 404 page), because the game has no devlog.
- Yields: post titles and dates, not a version field. Version is only recoverable by a developer-specific heuristic on titles.
- Auth: none. Rate limit: no `RateLimit`/`Retry-After` headers seen. No `ETag`, `Last-Modified` or `Cache-Control` on responses (checked response headers), so no conditional GET; the feed is small though.
- Stability: standard RSS, long-lived itch.io feature. [INFERENCE] Not documented in itch.io's API docs; it is a site feature, not an API contract.
- Weakness: a devlog post is not a release. A dev can post devlogs without a new build and can release a build without a post (S2's page "Updated" is 24 Sep, six days after the last devlog on 18 Sep).

### 2. Per-game `data.json`: `<game-url>/data.json`
- S1, S2, S3 all 200, JSON.
- Fields observed (all three samples): `id` (numeric game id), `title`, `authors[{name,url}]`, `cover_image`, `screenshots[]`, `tags[]` (slugs), `links{self, devlog?, comments?}`. S1 and S2 also have `links.devlog`/`links.comments`. S3 lacks them.
- **No version, no updated date, no uploads, no build info.** Only `title` can carry a version when the dev puts it in the title (S2: `My New Second Chance [EP33] [FREE]`).
- Use: resolve the numeric game id from the slug (needed by `wharf/latest`, optional there). Also a cheap way to read the title, which on S2 contains the version.
- Auth: none. Rate limit: none observed. Stability: undocumented (this is the endpoint used by itch.io's own embeds [INFERENCE]), so the shape could change; small fields used are low risk.

### 3. Server-side API: `/api/1/<key>/game/<id>/uploads`
- The current docs (https://itch.io/docs/api/serverside) and the legacy docs (https://itch.io/docs/api/serverside-legacy) list **no `uploads` endpoint at all**. Game-scoped endpoints documented are `download_keys`, `purchases`, `claimed-rewards`, `ownership` (scope `game:view:*`), which concern the game owner. The only game-listing endpoints are `profile/games` (`my-games`), which "Fetches data about all the games you've uploaded or have edit access to", and `profile/owned-keys` (games the user owns a key for).
- Auth: all `api.itch.io` endpoints require `Authorization: Bearer <API key or JWT>` (docs). Keys come from https://itch.io/api-keys; keys from user settings are unscoped. The legacy `/api/1/<key>/...` path is deprecated ("New integrations should use the modern serverside API").
- Live probe without a key: `https://itch.io/api/1/x/game/2525820/uploads` returned HTTP 200 body `{"errors":["invalid key"]}`; `https://api.itch.io/games/2525820` and `.../uploads` returned 401 `{"errors":["authentication required"]}`. I did not probe with a real key (none available, and I would not exercise the user's key from research). Therefore whether `games/<id>/uploads` works for games you do not own is **unverified**. [INFERENCE] It is the endpoint the itch app uses for games the user has a library entry for; for arbitrary third-party games expect it to be unavailable or restricted. Do not depend on it.
- Yields: would return upload objects (filename, size, `updated_at`), not a version string, per butler/itch app conventions [INFERENCE].

### 4. Butler channels and `user_version`: `https://api.itch.io/wharf/latest`
- Documented in https://itch.io/docs/api/serverside (section Wharf): returns `{"latest": "1.2.3"}`, the user-version of the newest build of a channel. **"This endpoint does not require authentication."** Parameters: `channel_name` (required), plus `game_id` or `target=user/game`. If the build has no user-version, `latest` is omitted. Private games return `invalid game`.
- Live probes (unauthenticated):
  - `target=nouser/nogame&channel_name=x` -> `{"errors":["invalid game"]}`
  - `target=abbys-cat/my-new-second-chance&channel_name=pc` -> `{}`: the channel `pc` exists (game is butler-pushed), but the build has no user-version.
  - Channels `windows`, `win`, `win64`, `windows-64`, `windows-stable`, `osx`, `linux`, `html5`, `default`, on S2; `win32` on S1 (by `target`) and on S2 (by `game_id`) -> `{"errors":["invalid channel"]}`.
- Consequences: channel names are chosen by the developer and **are not exposed on the public game page or `data.json`**; they cannot be enumerated without the owner's API. Many third-party F95-style devs upload by hand (S1's uploads are external MEGA/GDrive links, so no butler channel at all) and rarely set `--userversion`. Works only for the minority of games with butler channel plus user-version, and requires the user to supply the channel name per Game.
- Auth: none. Rate limit: none documented, none observed. Stability: documented, official.

### 5. Game page HTML
Fetched with default `curl` UA.
- S1: first request returned **HTTP 429** from Cloudflare (`server: cloudflare`, `content-type: text/html`, no `Retry-After`); the same URL succeeded 6 s later with 200. So HTML is the only method that hit a rate limit in these probes (I made one request per site-page pair a few seconds apart, three sites back to back). `data.json` and `devlog.rss` did not 429. Space page fetches out and back off on 429.
- Info table (`<td>Updated</td><td><abbr title="24 September 2026 @ 02:20 UTC">...`) gives an exact timestamp in the `abbr@title` attribute, and a relative "10 days ago" text. Present on S1 (`20 September 2026 @ 10:49 UTC`) and S2 (`24 September 2026 @ 02:20 UTC`), **absent on S3** (browser game with no uploads: the table starts at Status/Platforms).
- S1's Updated (20 Sep 10:49) equals the newest devlog `pubDate` exactly. S2's Updated (24 Sep) is later than the newest devlog (18 Sep), so "Updated" tracks the last edit of the page or uploads, not only devlog posts. [INFERENCE] Page edits (description, CGI gallery text, as the S2 page text itself lists) also bump it; so it can change with no new build.
- Uploads list (only for downloadable files): each `.upload` has `upload_name` title (e.g. `(new) MNSC - EP33 - PC`), size (`3.3 GB`), and for butler uploads a `build_row` with `<span class="version_name">Version 15</span>` plus `<span class="version_date"><abbr title="18 September 2026 @ 23:14 UTC">`. S2 shows `Version 15` (EP33 PC/MAC), `Version 13` (APK), `Version 1` (EP1-32 PC/MAC), `Version 2` (two APK). S1 has 12 uploads, all external links with no `build_row`; names carry the version ("Ch7", "Ch 1 Enhanced"). S3 has none.
- "Version N" is a per-channel/butler integer counter shown by itch.io, not a developer version string [INFERENCE from the observation that unrelated uploads show 1, 2, 13, 15]; it increases on each push and has an exact timestamp, but is absent for manually uploaded files and external links. Upload filenames carry the real version string only by developer convention.
- Auth: none. Stability: HTML markup, scraping-fragile; the `abbr title` format (`D Month YYYY @ HH:MM UTC`) is regular but unversioned.

### 6. Fanbox (one line)
`https://directorgames.fanbox.cc/` has no RSS/Atom feed (`/rss` and `/feed` return 404, no feed link in the HTML); the site's own JSON backend `https://api.fanbox.cc/post.listCreator?creatorId=directorgames&limit=3` answered 200 with `publishedDatetime`/`updatedDatetime` when called with an `Origin: https://directorgames.fanbox.cc` header (observed), but it is undocumented, so manual-only for fanbox is a sound call.

## Comparison

| Method | Version string? | Date? | Auth | Limits seen | Stability | Works on S1 / S2 / S3 |
|---|---|---|---|---|---|---|
| `devlog.rss` | Only if dev puts it in a post title (S2 yes, S1 no) | Post `pubDate` (exact) | none | none | RSS standard, site feature | yes / yes / **404** |
| `data.json` | Only via `title` (S2 `[EP33]`) | **no** | none | none | undocumented | yes / yes / yes |
| Server API `game/<id>/uploads` | not documented | n/a | API key required | n/a | legacy path deprecated; endpoint absent from docs | not usable unauthenticated; third-party access unverified |
| `wharf/latest` | `user_version` if dev set it, else `{}` | no | none | none | documented | needs channel name; S2 `pc` channel = `{}`; S1 external links (no channel) |
| Page HTML "Updated" | no | exact UTC (`abbr@title`) | none | **429 once** (S1) | scraping-fragile | yes / yes / absent |
| Page HTML upload `build_row` | "Version N" counter (butler uploads only) | exact per upload | none | same as HTML | scraping-fragile | no / yes / no |

## Recommendation

An itch.io Game has no reliable version string source; the best cheap and reliable signal is a change date. Rule for an **Update** of an itch.io Game:

1. Per Game, store the **Updated timestamp** from the page HTML `<td>Updated</td><td><abbr title="...">` (parse `D Month YYYY @ HH:MM UTC`). A later timestamp than the stored one is an Update. Show it as the version label (e.g. the date), because itch.io has no version field.
2. Enrich cheaply, never rely on it: in the same fetch, record the `<title>` (S2's `[EP33]` is a real version marker) and the first-listed upload names plus any `build_row` "Version N". If a version-looking token is present in the title or upload names, display it and prefer it for the label; changes in the set (title + upload names + highest `Version N`) are also an Update, which avoids false positives from description-only edits [INFERENCE: not validated across many games].
3. Cross-check/fallback: use `devlog.rss` (pubDate and titles) only to show "what changed" in the digest and as a backup signal when the HTML lacks "Updated". It 404s for games without a devlog (S3), so treat 404 as "no devlog", not an error.
4. Optional per-Game opt-in: if the user knows a butler channel name for a Game, call `api.itch.io/wharf/latest` (no auth) and use `latest` as an exact version. Do not build the server-side API key path: `uploads` is absent from the docs, needs a key, and third-party access is unverified.
5. Frequency: once per day, in the existing daily check, one HTML GET per itch.io Game. Sleep at least 3 s between itch.io fetches, send an honest User-Agent, and on HTTP 429 back off and retry later (observed once on the first request). A Game with no "Updated" row and no uploads (S3-type browser/HTML5 release) cannot be tracked automatically and should fall back to manual.
6. Fanbox stays manual.
