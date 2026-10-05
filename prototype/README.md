# F95 Tracker: core UI flows prototype (ticket #14)

This prototype is throwaway. It exists so you can react to layout and behaviour before the Go + templ app is built. Every page is static HTML with Tailwind (Play CDN), Alpine.js and htmx loaded from CDNs. There is no build step and nothing is saved: reloading a page resets it. All terms follow `CONTEXT.md`.

## Open it

```sh
python3 -m http.server 8000 -d prototype   # from the repo root
# then open http://localhost:8000/
```

You can also double-click `prototype/index.html`. Everything still works except the three real htmx swaps (Refresh on a Game, Save & check cookie, Send test notification), because browsers block `file://` requests. Those three show a notice explaining that.

The pages need internet access for the CDNs and the IBM Plex font.

## Visual system

The look is a dark-first "ledger": warm-tinted neutrals with a single amber accent. Body text uses IBM Plex Sans, and versions, ids and dates use IBM Plex Mono. **Theme** in the header switches to light mode. Every colour is a token in `assets/theme.css`, which `assets/tailwind-config.js` maps into Tailwind. The shared primitives are `.btn`, `.field`, `.chip`, `.seg` and `.panel`.

The striped bar at the top of every page and the dark pill switcher at the bottom are prototype chrome. They are not part of the design.

## Click paths

| Flow | Path |
|---|---|
| List → detail → mark played → Tag review | Games → *Corrupted Kingdoms* → **Mark version played** → **Mark played → review tags** → **Save review** (back to the Game) or **Skip for now** (to the tags-to-review queue) |
| Add Game → parse check | **Add Game** → **Use example link** → **Fetch thread** → parse check → **Confirm & add Game** |
| Import review | the "imported Games need a Play status check" strip on the list, or **Import review** in the nav |
| Settings | **Settings** in the nav |

## Pages

| Page | Shows |
|---|---|
| `index.html` — Game list | Filters: name, Play status, Dev status, tags (tap to include, tap again to exclude), rating, Behind, Update available. Sort options. An entry strip for the import review list and the tags-to-review queue. Per-Game badges: Update, Behind, details pending, Source unavailable, tags to review, check Play status. There is a no-match state and a first-run empty state (`?empty=1`). **Variants:** `?variant=A` ledger table (stacked rows on a phone), `?variant=B` grouped by Play status. |
| `game.html?id=…` — Game detail | Cover, name, Source link, Dev status, latest / last played / updated / last checked. **Refresh from Source** (htmx). Play status, with a hint saying whether this status alerts on Update. Rating from 0.5 to 5 in 0.5 steps, or none. Play log, where imported entries are undated and labelled "imported". **Mark version played**. Game tags grouped Present / Planned / Optional with ✓ confirmed / ✗ wrong / ○ unverified, origin (Genre + F95, Genre, F95 only, by hand), modifier notes, and *new* / *removed at source* flags. Adding a tag by hand. The raw Genre text. Ids to try: `ck` (rich), `wa` (details pending), `as` (Source unavailable, itch.io), `lr` (itch.io, tags added by hand), `tx` / `et` (in the queue). |
| `tag-review.html?id=…&version=…` — post-play Tag review | Present tags. Tags in both Genre and F95 start as correct; the rest start unset. Each is marked correct or wrong. Planned and optional tags sit in a separate collapsible section you can skip. **Skip for now** sends the Game to the queue. **Variants:** `?variant=A` checklist, `?variant=B` one tag at a time with large buttons for the phone. |
| `queue.html` — tags to review | Games whose review is pending or was skipped, with **Review now**. There is an empty state at `?empty=1`. |
| `add.html` — Add Game + add-time parse check | Source type: F95 thread, itch.io or manual link. Paste a URL or thread id, then fetch. The parse check highlights the Genre text and has three panels. **Needs a look** holds synonyms, phrases with no match (which become Custom tags), prose, and Possible / Planned items; each has an editable target, a qualifier, and accept / ignore. **Exact matches** are pre-accepted. **F95 only** lists F95 tags that are missing from the Genre text. Confirming states that no tag gets verified. Other states: invalid input, already tracked (thread id `31912`), and cookie invalid → add with details pending, set via the dashed "Prototype controls" box. |
| `settings.html` | Paste the raw Cookie header, with status valid / invalid and last checked (Save & check uses htmx). The alert Play-status set, with a count of affected Games. The Synonym table editor: add, edit, remove and filter. ntfy server and topic with **Send test notification** (htmx). |
| `import.html` — import review list | Games imported from `F95 Tracker - Sheet1.csv` with a derived Play status and the rule that produced it. The CSV flags are shown mapped to Dev status. You can filter by derived status, select rows, set a Play status in bulk, and confirm per row, for the selection, or for every row shown. Confirming removes rows, and there is a done state. |

**Switching variants:** use the arrows on the pill at the bottom, or the ← → keys. Variants are only on the Game list and the Tag review.

## Sample data

There are 12 Games, defined in `assets/data.js`. Their names, CSV versions, ratings and Dev-status columns come from the CSV. Everything else is invented: latest versions, tags, Genre text, dates, Play statuses, details pending and Source unavailable. *Apocalypse 2059* (`ap`) only appears through the Add Game flow.

## Screenshots

`screenshots/` holds desktop (1280 px), phone (390 px) and light-mode captures of every page and variant. Examples: `desktop-list-A.webp`, `mobile-tag-review-B.webp`, `desktop-add-parse.webp`, `light-game.webp`.

## Open design questions

### Game list

1. Which layout: the ledger table (A) or the list grouped by Play status (B)? Which sort should be the default? The prototype uses *recently updated at Source*.
2. A Game with an empty Play log is not Behind in the prototype. Is that right?
3. Should *dropped* or *finished* Games still show Behind / Update badges? *Campo* is dropped and shows Behind.
4. In the tag filter, tags marked wrong never match, while unverified tags do. Should planned and optional tags be filterable too?
5. Should the review list and the queue have entry strips on the list page, or are the nav badges enough?

### Game detail

6. Is a slider (0 = no rating) right for rating, or would tappable half-stars be better?
7. **Mark version played** prefills the latest version and today's date, and both can be edited. Should back-dated or older versions be allowed? Is "Skip" in the Tag review enough, or do you also want a "mark played, no review" shortcut?
8. Should tags removed at source stay visible and flagged until a review, or disappear by themselves?
9. A tag added by hand on the detail page starts unverified. Should it count as confirmed, since you added it yourself?
10. Can you set the Dev status by hand for itch.io and manual Sources?

### Add Game / parse check

11. Highlighted items are pre-set to their suggested default, and **Confirm** is always enabled. Should each highlighted item need an explicit decision first?
12. When you change a synonym or no-match target during the parse check, should there be a "save as Synonym" option so future parses reuse it?
13. Prose is ignored by default, and F95-only tags are added as present automatically. Are both of those defaults right?
14. If the cookie is invalid, the prototype offers "add with details pending". Should it block the add instead?
15. For itch.io: should the app fetch the name and version, or should it stay fully manual like a manual link?

### Tag review

16. Which layout: the checklist (A) or one tag at a time (B)?
17. Following the spec, only tags in both Genre and F95 are prefilled. Verdicts from an earlier review appear only as a "last review" hint. Should they prefill instead?
18. **Save** leaves unset tags unverified and takes the Game out of the queue. Should a partial review keep the Game in the queue?
19. For planned and optional tags, the choices are "now present" and "wrong". Are those the right verbs, and should "now present" change the qualifier to present?
20. If a review was skipped and you then play a newer version, should the Game have one queue entry (for the latest version) or one per play?

### Settings

21. Cookie: should the app show which account it is logged in as, alert you when the cookie turns invalid, and re-check it on a schedule?
22. The default alert set is *playing* + *on hold*. Is that right?
23. Should editing a Synonym re-apply it to existing Games, or only to future parses?
24. ntfy: does it need an auth token or priority setting? Should digest scheduling live here too?

### Import review

25. Are the derivation rules shown in the "derived:" line right? They are: rating + Completed/Abandoned → finished; On Hold → on hold; rating ≤ 2 → dropped; version but no rating → playing; no version → planned.
26. Should every imported Game go through review, or only the ones where the rule is uncertain?

### Overall

27. Is the dark-first warm "ledger" look right? On a phone the nav scrolls sideways. Would a bottom tab bar be better for digest click-throughs?
