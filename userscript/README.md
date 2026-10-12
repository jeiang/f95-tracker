# F95 Tracker userscript

Sends a thread's download links to the tracker when a download job is waiting for them (R-DL-3). Tested with Violentmonkey.

## Install
1. In Violentmonkey, create a new script and paste `f95-tracker-links.user.js` (or open the file's raw URL).
2. In the tracker, create an API token with scope `submit_links`.
3. On any F95zone thread, open the Violentmonkey menu: **set submit_links token** (paste it) and, if your tracker is not `https://f95.proxy.jeiang.dev`, **set tracker URL**.
4. If you change the tracker host, edit the `@connect` line to match (or keep `@connect *`).

## Use
1. In the tracker, press Download on a game; the job waits for links.
2. Open that game's F95 thread. A panel "Send links for <game> <version>" appears bottom right.
3. Press **Send links**. The script unmasks the first post's masked links using your own F95 session and collects the direct links with their platform.
4. If it says F95 wants a captcha, open one masked download link, solve the captcha on F95 yourself, return and press the button again. The script never solves captchas.
5. The panel shows how many links were accepted and how many need manual handling (unsupported hosts).

Cookies and the token go only to the configured tracker URL; masked URLs are never sent.

## Test
`node userscript/extract.test.js` (from the repo root) checks link and platform extraction against `testdata/f95/`.
