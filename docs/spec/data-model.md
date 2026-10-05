# Data model (PROPOSAL for ticket #11)

Status: Final (resolved in #11). Terms follow `CONTEXT.md`. Ticket refs are `#n`; #11 means decided in this grilling; #14 is the UI prototype resolution.

Conventions (accepted in #11): goose-style migration, `STRICT` tables, timestamps as UTC ISO-8601 `TEXT`, booleans `INTEGER CHECK (x IN (0,1))`, integer surrogate keys. Stack fixed by #4 (modernc sqlite, goose, sqlc); `PRAGMA foreign_keys=ON`, `journal_mode=WAL`.

## Schema

```sql
-- +goose Up

-- ============ Games and Sources ============

CREATE TABLE game (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL,                       -- F95 title for F95 Games, CSV/hand name otherwise (#10)
  play_status   TEXT NOT NULL DEFAULT 'planned'
                CHECK (play_status IN ('planned','playing','finished','dropped','on_hold')),
  rating_x2     INTEGER CHECK (rating_x2 BETWEEN 1 AND 10),  -- 0.5..5 in 0.5 steps stored x2; NULL = no rating (CONTEXT)
  cover_path    TEXT,                                -- cached image file, relative to the state dir (#11)
  cover_source_url TEXT,                             -- where the cached file was fetched from
  cover_fetched_at TEXT,
  platform_pref TEXT CHECK (platform_pref IN ('linux','win_linux','win')),  -- per-Game override of default (#13)
  import_review INTEGER NOT NULL DEFAULT 0 CHECK (import_review IN (0,1)),  -- on the CSV-import review list (#10)
  added_at      TEXT NOT NULL,
  updated_at    TEXT NOT NULL
) STRICT;
CREATE INDEX game_play_status ON game(play_status);
CREATE INDEX game_import_review ON game(id) WHERE import_review = 1;

CREATE TABLE source (
  id               INTEGER PRIMARY KEY,
  game_id          INTEGER NOT NULL REFERENCES game(id) ON DELETE CASCADE,
  kind             TEXT NOT NULL CHECK (kind IN ('f95_thread','itchio','manual')),
  is_primary       INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0,1)),   -- many Sources per Game, exactly one primary (#11); only the primary is checked
  external_id      TEXT,                              -- F95 thread id; itch.io URL slug; NULL for manual
  url              TEXT NOT NULL,
  -- version / update detection (see "Update detection")
  latest_version   TEXT,                              -- raw version string/token as last seen; NULL when the Source reports only a date (itch.io without a version token, #7, #11)
  change_key       TEXT,                              -- compared EXACTLY between checks (F95: = latest_version; itch.io: fingerprint, #7)
  latest_version_norm TEXT GENERATED ALWAYS AS (
    lower(CASE WHEN lower(substr(trim(latest_version, ' '||char(9,10,13)),1,1)) = 'v'
               THEN substr(trim(latest_version, ' '||char(9,10,13)),2)
               ELSE trim(latest_version, ' '||char(9,10,13)) END)) VIRTUAL,
  dev_status       TEXT CHECK (dev_status IN ('ongoing','completed','abandoned','on_hold')),  -- fetched for F95; user-edited for manual/itch
  thread_updated_at TEXT,                             -- F95 "Thread Updated" / itch "Updated" timestamp (informational)
  -- check bookkeeping (#8)
  last_checked_at  TEXT,                              -- last successful checker/page answer
  last_detail_at   TEXT,                              -- last logged-in detail fetch (drives weekly rolling refresh)
  miss_count       INTEGER NOT NULL DEFAULT 0 CHECK (miss_count >= 0),  -- consecutive daily runs absent from checker.php
  details_pending  INTEGER NOT NULL DEFAULT 0 CHECK (details_pending IN (0,1)),  -- added with guest data, cookie invalid (#8)
  unavailable_at   TEXT,                              -- set after 3 misses + confirming fetch; excluded from checks
  unavailable_reason TEXT,
  checks_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (checks_enabled IN (0,1)),  -- manual re-enable clears unavailable_at (#8)
  created_at       TEXT NOT NULL,
  CHECK (kind <> 'f95_thread' OR external_id IS NOT NULL),
  CHECK (kind <> 'manual' OR change_key IS NULL),     -- manual Sources are never checked
  CHECK (is_primary = 1 OR (latest_version IS NULL AND change_key IS NULL AND dev_status IS NULL  -- non-primary = link only (#11)
         AND thread_updated_at IS NULL AND last_checked_at IS NULL AND last_detail_at IS NULL
         AND miss_count = 0 AND details_pending = 0 AND unavailable_at IS NULL))
) STRICT;
CREATE UNIQUE INDEX source_kind_external ON source(kind, external_id) WHERE external_id IS NOT NULL;  -- thread = exactly one Game (CONTEXT, #10)
CREATE UNIQUE INDEX source_one_primary ON source(game_id) WHERE is_primary = 1;
CREATE INDEX source_checkable ON source(kind) WHERE is_primary = 1 AND checks_enabled = 1 AND unavailable_at IS NULL AND kind <> 'manual';

-- ============ Play log (user may edit/delete entries, #11) ============

CREATE TABLE play_log (
  id         INTEGER PRIMARY KEY,                     -- tie-break for equal dates: higher id
  game_id    INTEGER NOT NULL REFERENCES game(id) ON DELETE CASCADE,
  version    TEXT NOT NULL,
  version_norm TEXT GENERATED ALWAYS AS (
    lower(CASE WHEN lower(substr(trim(version, ' '||char(9,10,13)),1,1)) = 'v'
               THEN substr(trim(version, ' '||char(9,10,13)),2)
               ELSE trim(version, ' '||char(9,10,13)) END)) VIRTUAL,
  played_on  TEXT,                                    -- date (YYYY-MM-DD); NULL for imported rows (#10)
  origin     TEXT NOT NULL CHECK (origin IN ('user','imported')),
  created_at TEXT NOT NULL,
  CHECK (origin <> 'user' OR played_on IS NOT NULL)   -- user entries stay dated; edits cannot clear the date
) STRICT;
CREATE INDEX play_log_game ON play_log(game_id, played_on DESC, id DESC);

-- Last played = newest dated entry, else the newest imported (undated) entry. Empty log: no row.
CREATE VIEW game_last_played AS
SELECT game_id, id AS play_log_id, version, version_norm, played_on
FROM (SELECT p.*, ROW_NUMBER() OVER (PARTITION BY game_id
        ORDER BY played_on IS NULL, played_on DESC, id DESC) AS rn FROM play_log p)
WHERE rn = 1;

-- Behind, evaluated against the primary Source only. Empty Play log = not Behind (#14).
-- UI shows the badge only for Games whose Play status is in the alert set (#14).
CREATE VIEW game_behind AS
SELECT g.id AS game_id, s.latest_version, lp.version AS last_played, lp.played_on
FROM game g
JOIN source s ON s.game_id = g.id AND s.is_primary = 1
JOIN game_last_played lp ON lp.game_id = g.id
WHERE (s.latest_version_norm IS NOT NULL AND s.latest_version_norm <> lp.version_norm)       -- version-bearing Source
   OR (s.latest_version IS NULL AND s.thread_updated_at IS NOT NULL                          -- date-only Source (#7, #11)
       AND lp.played_on IS NOT NULL AND date(s.thread_updated_at) > lp.played_on);

-- ============ Tag vocabulary, synonyms, Game tags ============

CREATE TABLE tag (
  id    INTEGER PRIMARY KEY,
  kind  TEXT NOT NULL CHECK (kind IN ('f95','custom')),
  slug  TEXT NOT NULL,                                -- F95 slug (auto-grown, #9) or custom slug (BBW, Threesome)
  label TEXT NOT NULL,
  UNIQUE (kind, slug)
) STRICT;

CREATE TABLE synonym (                                -- Genre phrase -> tag; editable, seeded from research SYN (#9)
  id         INTEGER PRIMARY KEY,
  phrase_key TEXT NOT NULL UNIQUE,                    -- normalized key (parser rule 3)
  tag_id     INTEGER NOT NULL REFERENCES tag(id) ON DELETE CASCADE,
  origin     TEXT NOT NULL CHECK (origin IN ('seed','user')),  -- user = saved from a mapping fix
  created_at TEXT NOT NULL
) STRICT;

CREATE TABLE game_tag (
  id               INTEGER PRIMARY KEY,
  game_id          INTEGER NOT NULL REFERENCES game(id) ON DELETE CASCADE,
  tag_id           INTEGER NOT NULL REFERENCES tag(id),
  origin           TEXT NOT NULL CHECK (origin IN ('f95_list','genre','both','manual')),
  qualifier        TEXT NOT NULL DEFAULT 'present' CHECK (qualifier IN ('present','planned','optional')),
  verification     TEXT NOT NULL DEFAULT 'unverified' CHECK (verification IN ('unverified','confirmed','wrong')),
  modifier_note    TEXT,                              -- mild / soft / light (#9)
  source_phrase    TEXT,                              -- raw Genre phrase that produced it; NULL if not from Genre
  mapping_override INTEGER NOT NULL DEFAULT 0 CHECK (mapping_override IN (0,1)),  -- user re-pointed phrase -> tag; also saved as synonym
  is_new           INTEGER NOT NULL DEFAULT 0 CHECK (is_new IN (0,1)),            -- arrived via refresh, unreviewed
  promoted         INTEGER NOT NULL DEFAULT 0 CHECK (promoted IN (0,1)),          -- planned/optional auto-promoted to present; ask next review
  removed_at_source_at TEXT,                          -- gone from Source on refresh; row kept (#9)
  f95_only         INTEGER NOT NULL DEFAULT 0 CHECK (f95_only IN (0,1)),          -- in F95 list, absent from Genre text
  verified_at      TEXT,
  UNIQUE (game_id, tag_id),                           -- duplicates collapsed; same tag present+planned -> present
  CHECK (verification = 'unverified' OR verified_at IS NOT NULL),
  CHECK (origin = 'manual' OR origin = 'f95_list' OR source_phrase IS NOT NULL)
) STRICT;
CREATE INDEX game_tag_tag ON game_tag(tag_id);
CREATE INDEX game_tag_unreviewed ON game_tag(game_id) WHERE verification = 'unverified' OR is_new = 1 OR promoted = 1;

CREATE TABLE tag_review (                             -- one row per Game; queue = state IN ('pending','skipped')
  game_id                   INTEGER PRIMARY KEY REFERENCES game(id) ON DELETE CASCADE,
  state                     TEXT NOT NULL CHECK (state IN ('pending','skipped','done')),
  last_reviewed_play_log_id INTEGER REFERENCES play_log(id),   -- later reviews cover only tags new/changed since
  reviewed_at               TEXT,
  updated_at                TEXT NOT NULL,
  CHECK (state <> 'done' OR reviewed_at IS NOT NULL)
) STRICT;
CREATE INDEX tag_review_queue ON tag_review(state) WHERE state <> 'done';

-- ============ Settings and credentials ============

CREATE TABLE settings (                               -- single row
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  mirror_host_order   TEXT NOT NULL DEFAULT '["pixeldrain","mega","gofile"]',  -- JSON array (#13)
  platform_pref       TEXT NOT NULL DEFAULT 'linux' CHECK (platform_pref IN ('linux','win_linux','win')), -- default order Linux > Win/Linux > Win
  updated_at          TEXT NOT NULL
) STRICT;

CREATE TABLE alert_play_status (                      -- configured alert set (CONTEXT: Update)
  play_status TEXT PRIMARY KEY CHECK (play_status IN ('planned','playing','finished','dropped','on_hold'))
) STRICT;
INSERT INTO alert_play_status(play_status) VALUES ('playing'),('on_hold'),('planned');  -- default alert set (#14, #11)

CREATE TABLE f95_credential (                         -- single row; plaintext by decision (#8)
  id               INTEGER PRIMARY KEY CHECK (id = 1),
  cookie_jar       TEXT NOT NULL,                     -- JSON {name: value} for xf_user, xf_tfa_trust, xf_session, xf_csrf; rotated Set-Cookie persisted
  user_agent       TEXT NOT NULL,                     -- browser UA captured at paste, sent on all F95 requests
  validity         TEXT NOT NULL DEFAULT 'unknown' CHECK (validity IN ('unknown','valid','invalid')),
  validated_at     TEXT,                              -- last data-logged-in="true" probe
  tfa_trust_expires_at TEXT,                          -- parsed from paste if available; drives refresh banner (#2)
  invalid_alerted_at TEXT,                            -- one ntfy alert per valid->invalid transition
  updated_at       TEXT NOT NULL
) STRICT;

CREATE TABLE api_token (                              -- personal tokens, shown once, stored hashed
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,
  scope        TEXT NOT NULL CHECK (scope IN ('submit_links','downloader')),  -- userscript / artemis worker (#13)
  token_hash   BLOB NOT NULL UNIQUE,                  -- SHA-256 of a >=256-bit random token. sha256 (accepted; high-entropy token)
  created_at   TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at   TEXT
) STRICT;

-- Sessions: scs sqlite3store schema, revocable (#11). 30-day sliding lifetime (#12); expiry is a Julian-day REAL as scs writes it.
CREATE TABLE sessions (
  token  TEXT PRIMARY KEY,
  data   BLOB NOT NULL,
  expiry REAL NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions(expiry);

-- ============ Checks ============

CREATE TABLE check_run (
  id          INTEGER PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN ('daily','import_backfill','manual')),
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','ok','partial','failed')),
  f95_stopped INTEGER NOT NULL DEFAULT 0 CHECK (f95_stopped IN (0,1)),  -- retries exhausted / block signal; "check failed" in digest (#8)
  CHECK ((status = 'running') = (finished_at IS NULL))
) STRICT;

CREATE TABLE check_result (
  id          INTEGER PRIMARY KEY,
  run_id      INTEGER NOT NULL REFERENCES check_run(id) ON DELETE CASCADE,
  source_id   INTEGER REFERENCES source(id) ON DELETE CASCADE,       -- NULL for the cookie probe
  step        TEXT NOT NULL CHECK (step IN ('checker','itch_page','detail','cookie_probe')),
  outcome     TEXT NOT NULL CHECK (outcome IN ('unchanged','update','miss','fetched','skipped','error')),
  old_key     TEXT,                                   -- change_key before (Update = old_key <> new_key)
  new_key     TEXT,
  http_status INTEGER,
  attempts    INTEGER NOT NULL DEFAULT 1,
  error       TEXT,
  at          TEXT NOT NULL,
  CHECK (outcome <> 'error' OR error IS NOT NULL),
  CHECK (outcome <> 'update' OR (old_key IS NOT NULL AND new_key IS NOT NULL AND old_key <> new_key)),
  CHECK (step = 'cookie_probe' OR source_id IS NOT NULL)
) STRICT;
CREATE INDEX check_result_run ON check_result(run_id);
CREATE INDEX check_result_source ON check_result(source_id, at DESC);

CREATE TABLE detail_fetch_queue (                     -- persisted so cookie-invalid / blocked runs resume (#8)
  source_id   INTEGER PRIMARY KEY REFERENCES source(id) ON DELETE CASCADE,  -- at most one pending fetch per Source
  reason      TEXT NOT NULL CHECK (reason IN ('added','update','weekly','manual','import')),
  budget      TEXT NOT NULL CHECK (budget IN ('routine','import')),         -- routine cap 40/day; import has own budget
  enqueued_at TEXT NOT NULL
) STRICT;

-- ============ Notifications ============

CREATE TABLE notification (
  id        INTEGER PRIMARY KEY,
  kind      TEXT NOT NULL CHECK (kind IN ('digest','cookie_invalid','source_unavailable','download_needs_human')),
  run_id    INTEGER REFERENCES check_run(id) ON DELETE SET NULL,
  title     TEXT NOT NULL,
  body      TEXT NOT NULL,
  sent_at   TEXT,                                      -- NULL = pending/failed
  error     TEXT,
  created_at TEXT NOT NULL
) STRICT;

CREATE TABLE notification_item (                      -- what a message covered; dedupes "one alert" rules and digest rows
  notification_id  INTEGER NOT NULL REFERENCES notification(id) ON DELETE CASCADE,
  game_id          INTEGER REFERENCES game(id) ON DELETE CASCADE,
  check_result_id  INTEGER REFERENCES check_result(id) ON DELETE SET NULL,
  download_job_id  INTEGER,                            -- FK added below (download_job defined after)
  UNIQUE (notification_id, check_result_id)
) STRICT;

-- ============ Downloader (contract only, #13) ============

CREATE TABLE download_job (
  id             INTEGER PRIMARY KEY,
  game_id        INTEGER NOT NULL REFERENCES game(id) ON DELETE CASCADE,
  source_id      INTEGER NOT NULL REFERENCES source(id) ON DELETE CASCADE,
  target_version TEXT NOT NULL,                        -- version string at creation; destination dir ~/Games/<Game>/<version>
  trigger        TEXT NOT NULL CHECK (trigger IN ('update','button')),
  state          TEXT NOT NULL DEFAULT 'awaiting_links'
                 CHECK (state IN ('awaiting_links','queued','downloading','extracting','done','needs_human','cancelled')),
  needs_human_reason TEXT,
  active_mirror_id INTEGER,                            -- mirror currently attempted
  bytes_done     INTEGER,
  bytes_total    INTEGER,
  result_path    TEXT,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  CHECK (state <> 'needs_human' OR needs_human_reason IS NOT NULL)
) STRICT;
-- One open job per (game, version) (accepted in #11); done/cancelled jobs are history.
CREATE UNIQUE INDEX download_job_open ON download_job(game_id, target_version) WHERE state NOT IN ('done','cancelled');
CREATE INDEX download_job_state ON download_job(state);

CREATE TABLE download_mirror (
  id        INTEGER PRIMARY KEY,
  job_id    INTEGER NOT NULL REFERENCES download_job(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,                          -- ordered list; settings.mirror_host_order applied at insert
  host      TEXT NOT NULL,
  url       TEXT NOT NULL,                             -- resolved (unmasked) URL only; masked URLs are never stored
  platform  TEXT CHECK (platform IN ('linux','win_linux','win','other')),
  supported INTEGER NOT NULL CHECK (supported IN (0,1)),  -- 0 = manual link kept on the job
  state     TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','active','failed','done','manual')),
  error     TEXT,
  attempts  INTEGER NOT NULL DEFAULT 0,
  UNIQUE (job_id, position),
  CHECK (supported = 1 OR state = 'manual')
) STRICT;

CREATE TABLE download_job_transition (                -- audit of state changes, written in the same tx as the change
  id         INTEGER PRIMARY KEY,
  job_id     INTEGER NOT NULL REFERENCES download_job(id) ON DELETE CASCADE,
  from_state TEXT,
  to_state   TEXT NOT NULL,
  actor      TEXT NOT NULL CHECK (actor IN ('tracker','downloader','user')),
  detail     TEXT,
  at         TEXT NOT NULL
) STRICT;
CREATE INDEX download_job_transition_job ON download_job_transition(job_id, id);

-- +goose Down
-- (drop all of the above in reverse order)
```

Notes on the DDL:
- `notification_item.download_job_id` should be declared `REFERENCES download_job(id) ON DELETE SET NULL` by reordering the two tables in the real migration; shown loose here only for reading order.
- Allowed `download_job` transitions are enforced in Go (single state-machine function), not triggers: `awaiting_links→queued→downloading→extracting→done`; any of `queued/downloading/extracting→queued` (mirror fall-through); `*→needs_human`; `needs_human→done|queued` (user action); `awaiting_links|queued|downloading|extracting|needs_human→cancelled` (user action; the downloader sees `cancelled` on its next poll and stops). `done` and `cancelled` are terminal. Accepted in #11.

## Update detection

Two different comparisons, two different column sets (#8, #2, CONTEXT).

| Concept | Rule | Columns |
|---|---|---|
| **Update** (F95) | Exact byte-string inequality of the `checker.php` version vs the stored one. No trimming, case-folding, or parsing (versions are free-form, #2). | `source.change_key` (= raw F95 version) compared by the app; result recorded as `check_result(outcome='update', old_key, new_key)`; then `source.latest_version`/`change_key` overwritten. |
| **Update** (itch.io) | Inequality of a fingerprint built from the `Updated` timestamp + title/upload names + highest `Version N` (#7). | `source.change_key` = fingerprint; `source.latest_version` = version token if found, else NULL (UI shows the date from `thread_updated_at`); `thread_updated_at` = timestamp. |
| **Behind** (versioned Source) | `last played ≠ latest`, both lower-cased, surrounding whitespace trimmed, one leading `v` stripped (#8, CONTEXT). Last played = newest dated Play log entry, else the imported entry. | `play_log.version_norm` (via view `game_last_played`) vs `source.latest_version_norm`; both VIRTUAL generated columns; view `game_behind`. |
| **Behind** (date-only Source) | Primary Source `thread_updated_at` is later than the newest dated Play log entry (`latest_version` is NULL). No dated entry = not Behind (#11, CONTEXT). | `source.thread_updated_at` vs `game_last_played.played_on`; view `game_behind`. |

Only the primary Source of a Game is checked; non-primary Sources are links and none of the fields below are used for them (#11). Flow per primary Source in a run: answer received → `last_checked_at`, `miss_count=0` → if `change_key` differs: write `check_result` (update), set new values, enqueue `detail_fetch_queue(reason='update')`, and if `game.play_status IN alert_play_status` create `download_job` (`awaiting_links`) and a `notification_item` for the digest. Import baseline (#10): `latest_version`/`change_key` are written at import with no `check_result`, so no pre-import Update. Missing from `checker.php`: `miss_count+1`; at 3 → one confirming detail fetch → `unavailable_at` set + `notification(kind='source_unavailable')`; the Source leaves `source_checkable` until `checks_enabled`/`unavailable_at` is reset by hand (#8).

`lower()`/`trim()` are SQLite ASCII-only; the Go normalizer MUST match (one shared test vector list; accepted in #11).

## Invariants

1. Play status is one of planned/playing/finished/dropped/on_hold, set only by the user (never by checks), except the CSV import derivation (CONTEXT, #10). `game.play_status` CHECK; no code path from checks writes it.
2. Dev status lives on `source` and comes from the primary Source; fetched, never user-set, for F95 Sources; user-edited only for non-F95 Sources (#14, #10, #11); CSV flags apply only to non-F95 and converted rows, F95 live data wins (CONTEXT, #10). `source.dev_status`.
3. An F95 thread is exactly one Game; a sequel on another thread is another Game (CONTEXT, #10). `UNIQUE(kind, external_id)`; a Game has exactly one primary Source (`source_one_primary`); others are links only and carry no check fields (CONTEXT, #11).
4. Rating is NULL or 0.5–5 in 0.5 steps (CONTEXT). `rating_x2 BETWEEN 1 AND 10`.
5. Play log entries may be corrected (date, version) or deleted by the user; user entries stay dated; last played = newest dated entry, else the imported entry; imported rows have NULL date and origin `imported`; one row per CSV row, same-thread merges become earlier entries (CONTEXT, #10, #11). View `game_last_played`.
6. Update = exact change of the primary Source's version string; Behind = normalized inequality, or for date-only Sources, updated after the newest dated entry; empty Play log is not Behind (#8, #11, #14, CONTEXT). See above.
7. Alerts (digest rows, download jobs) and Update/Behind badges only for Games whose Play status is in `alert_play_status`, default playing, on_hold, planned (CONTEXT, #13, #14, #11).
8. F95 version strings are never parsed or ordered (#2). Only equality is used.
9. The daily check uses no cookie (`checker.php`); detail fetches (tags, Genre, cover, Dev status) use the stored cookie + UA (#2, #3, #8).
10. A Source is marked unavailable only after 3 consecutive misses plus a confirming detail fetch; unavailable Sources are excluded from checks until re-enabled by hand (#8).
11. Invalid cookie: `validity='invalid'`, exactly one alert per transition (`invalid_alerted_at`), detail fetches stay in `detail_fetch_queue`, new Games get `details_pending=1` (#8).
12. Rotated `Set-Cookie` values are persisted into `f95_credential.cookie_jar` (#8). Cookie is plaintext (decision, #8); the app never sends it to the downloader (#6, #13).
13. A Game tag refers to exactly one tag (F95 or custom); one row per (Game, tag); present+planned collapses to present (#9). `UNIQUE(game_id, tag_id)`.
14. Qualifier ∈ present/planned/optional; origin ∈ f95_list/genre/both/manual; verification ∈ unverified/confirmed/wrong (CONTEXT, #9).
15. Verification is set only by the user: in a Tag review, by hand on any tag of any qualifier outside a review, or by adding a tag by hand (starts `confirmed`, any Game); add-time confirmation never verifies. A planned/optional tag marked "now present" in a review becomes qualifier `present` + `confirmed`. Tags marked `wrong` never match tag filters (CONTEXT, #9, #14, #11).
16. Refresh merges: user verifications and mapping overrides persist; new tags arrive `unverified` + `is_new`; tags gone from the Source are kept with `removed_at_source_at`; a planned/optional tag that appears present is promoted and flagged `promoted` (#9).
17. F95 tags absent from Genre text are `f95_only` (#9).
18. One `tag_review` row per Game; `pending`/`skipped` rows form the tags-to-review queue, all imported F95 Games start `pending`; a review prefills earlier verdicts, and the Game leaves the queue (`done`) only when every present tag has a verdict (partial save keeps it queued) (#9, #10, #14, #11).
19. A mapping fix saves a `synonym` (origin `user`); the synonym table is seeded from research `SYN`; creating or editing a synonym re-applies it to `unverified`, non-`mapping_override` tags on existing Games, leaving verified and overridden tags untouched (#9, #14, #11).
20. Vocabulary grows automatically from every logged-in F95 tag list; custom tags exist only for phrases without an F95 slug (#9).
21. Every imported Game has `import_review=1` until the user clears it (bulk edit) (#10).
22. Import is idempotent by thread id / link (#10); `UNIQUE(kind, external_id)` is the key.
23. Detail-fetch queue: one entry per Source; routine budget 40/day, import has its own budget (#8).
24. API tokens are stored only as hashes, scoped, and revocable; a revoked token authenticates nothing (#13).
25. Download job holds an ordered mirror list of resolved URLs only; unsupported hosts stay as `manual` mirrors; no supported mirror or all failed → `needs_human` with a reason and one notification; `done` only after verify + extract (#13).
26. Versions are kept on disk per `<Game>/<version>`; no table is ever pruned: checks, notifications, jobs, and Play log history are kept forever (#13, #11). A `cancelled` job is terminal and stops the downloader on its next poll (#11).
27. Sessions are rows in the scs `sessions` table (revocable), 30-day sliding; only OIDC subjects in the allowlist (module option, not DB) get a session (#12, #11).
28. Backups use `VACUUM INTO` of a consistent DB; state is the one SQLite file plus cached cover files in the state dir (#12, #11).
