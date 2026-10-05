# Data model (PROPOSAL for ticket #11)

Status: draft for the grilling. Anything not dictated by a resolved ticket is marked **PROPOSED** with the alternative in one line. Terms follow `CONTEXT.md`. Ticket refs are `#n`.

Conventions (PROPOSED): goose-style migration, `STRICT` tables, timestamps as UTC ISO-8601 `TEXT`, booleans `INTEGER CHECK (x IN (0,1))`, integer surrogate keys. Alternative: unix-epoch integers for timestamps. Stack fixed by #4 (modernc sqlite, goose, sqlc); `PRAGMA foreign_keys=ON`, `journal_mode=WAL`.

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
  cover_url     TEXT,                                -- PROPOSED: URL only. Alt: cache image file in state dir
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
  is_primary       INTEGER NOT NULL DEFAULT 1 CHECK (is_primary IN (0,1)),   -- PROPOSED: many Sources allowed, one primary. Alt: UNIQUE(game_id)
  external_id      TEXT,                              -- F95 thread id; itch.io URL slug; NULL for manual
  url              TEXT NOT NULL,
  -- version / update detection (see "Update detection")
  latest_version   TEXT,                              -- raw string as last seen; display + Behind input
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
  CHECK (kind <> 'manual' OR change_key IS NULL)      -- manual Sources are never checked
) STRICT;
CREATE UNIQUE INDEX source_kind_external ON source(kind, external_id) WHERE external_id IS NOT NULL;  -- thread = exactly one Game (CONTEXT, #10)
CREATE UNIQUE INDEX source_one_primary ON source(game_id) WHERE is_primary = 1;
CREATE INDEX source_checkable ON source(kind) WHERE checks_enabled = 1 AND unavailable_at IS NULL AND kind <> 'manual';

-- ============ Play log (append-only) ============

CREATE TABLE play_log (
  id         INTEGER PRIMARY KEY,                     -- order = insertion order; newest id = last played
  game_id    INTEGER NOT NULL REFERENCES game(id) ON DELETE CASCADE,
  version    TEXT NOT NULL,
  version_norm TEXT GENERATED ALWAYS AS (
    lower(CASE WHEN lower(substr(trim(version, ' '||char(9,10,13)),1,1)) = 'v'
               THEN substr(trim(version, ' '||char(9,10,13)),2)
               ELSE trim(version, ' '||char(9,10,13)) END)) VIRTUAL,
  played_on  TEXT,                                    -- NULL for imported rows (#10)
  origin     TEXT NOT NULL CHECK (origin IN ('user','imported')),
  created_at TEXT NOT NULL,
  CHECK (origin <> 'user' OR played_on IS NOT NULL)   -- PROPOSED: user entries always dated. Alt: allow undated
) STRICT;
CREATE INDEX play_log_game ON play_log(game_id, id DESC);
-- +goose StatementBegin
CREATE TRIGGER play_log_no_update BEFORE UPDATE ON play_log BEGIN SELECT RAISE(ABORT,'play_log is append-only'); END;
CREATE TRIGGER play_log_no_delete BEFORE DELETE ON play_log
  WHEN EXISTS (SELECT 1 FROM game WHERE id = OLD.game_id)       -- allows ON DELETE CASCADE of a Game
  BEGIN SELECT RAISE(ABORT,'play_log is append-only'); END;
-- +goose StatementEnd

-- Behind: last played (newest Play log entry) vs latest version of the primary Source.
CREATE VIEW game_behind AS
SELECT g.id AS game_id, s.latest_version, p.version AS last_played
FROM game g
JOIN source s   ON s.game_id = g.id AND s.is_primary = 1
JOIN play_log p ON p.id = (SELECT MAX(id) FROM play_log WHERE game_id = g.id)
WHERE s.latest_version_norm IS NOT NULL AND s.latest_version_norm <> p.version_norm;

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
) STRICT;                                              -- PROPOSED default rows: playing, on_hold. Alt: playing only

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
  token_hash   BLOB NOT NULL UNIQUE,                  -- SHA-256 of a >=256-bit random token. PROPOSED sha256. Alt: argon2 (pointless for high-entropy)
  created_at   TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at   TEXT
) STRICT;
-- Sessions: none (stateless signed cookie, 30-day sliding, #12). See open questions.

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
                 CHECK (state IN ('awaiting_links','queued','downloading','extracting','done','needs_human')),
  needs_human_reason TEXT,
  active_mirror_id INTEGER,                            -- mirror currently attempted
  bytes_done     INTEGER,
  bytes_total    INTEGER,
  result_path    TEXT,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  CHECK (state <> 'needs_human' OR needs_human_reason IS NOT NULL)
) STRICT;
-- PROPOSED: one open job per (game, version). Alt: allow duplicates, UI warns.
CREATE UNIQUE INDEX download_job_open ON download_job(game_id, target_version) WHERE state <> 'done';
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
- Allowed `download_job` transitions are enforced in Go (single state-machine function), not triggers: `awaiting_links→queued→downloading→extracting→done`; any of `queued/downloading/extracting→queued` (mirror fall-through) ; `*→needs_human`; `needs_human→done|queued` (user action). PROPOSED; alt: SQLite trigger table.

## Update detection

Two different comparisons, two different column sets (#8, #2, CONTEXT).

| Concept | Rule | Columns |
|---|---|---|
| **Update** (F95) | Exact byte-string inequality of the `checker.php` version vs the stored one. No trimming, case-folding, or parsing (versions are free-form, #2). | `source.change_key` (= raw F95 version) compared by the app; result recorded as `check_result(outcome='update', old_key, new_key)`; then `source.latest_version`/`change_key` overwritten. |
| **Update** (itch.io) | Inequality of a fingerprint built from the `Updated` timestamp + title/upload names + highest `Version N` (#7). | `source.change_key` = fingerprint; `source.latest_version` = version token if found else the date; `thread_updated_at` = timestamp. |
| **Behind** | `last played ≠ latest`, both lower-cased, surrounding whitespace trimmed, one leading `v` stripped (#8, CONTEXT). | `play_log.version_norm` (newest row) vs `source.latest_version_norm`; both VIRTUAL generated columns; exposed by view `game_behind`. |

Flow per Source in a run: answer received → `last_checked_at`, `miss_count=0` → if `change_key` differs: write `check_result` (update), set new values, enqueue `detail_fetch_queue(reason='update')`, and if `game.play_status IN alert_play_status` create `download_job` (`awaiting_links`) and a `notification_item` for the digest. Import baseline (#10): `latest_version`/`change_key` are written at import with no `check_result`, so no pre-import Update. Missing from `checker.php`: `miss_count+1`; at 3 → one confirming detail fetch → `unavailable_at` set + `notification(kind='source_unavailable')`; the Source leaves `source_checkable` until `checks_enabled`/`unavailable_at` is reset by hand (#8).

PROPOSED: `lower()`/`trim()` are SQLite ASCII-only; the Go normalizer MUST match (one shared test vector list). Alt: compute Behind only in Go and drop the generated columns.

## Invariants

1. Play status is one of planned/playing/finished/dropped/on_hold, set only by the user (never by checks), except the CSV import derivation (CONTEXT, #10). `game.play_status` CHECK; no code path from checks writes it.
2. Dev status is fetched, never user-set, for F95 Sources; user-set only on manual/itch.io Sources; CSV flags apply only to non-F95 and converted rows, F95 live data wins (CONTEXT, #10). `source.dev_status`.
3. An F95 thread is exactly one Game; a sequel on another thread is another Game (CONTEXT, #10). `UNIQUE(kind, external_id)`; a Game has one primary Source (`source_one_primary`).
4. Rating is NULL or 0.5–5 in 0.5 steps (CONTEXT). `rating_x2 BETWEEN 1 AND 10`.
5. Play log is append-only; newest entry = last played; imported rows have NULL date and origin `imported`; one row per CSV row, same-thread merges become earlier entries (CONTEXT, #10). Triggers + CHECK.
6. Update = exact change of the version string; Behind = normalized inequality (#8, CONTEXT). See above.
7. Alerts (digest rows, download jobs) only for Games whose Play status is in `alert_play_status` (CONTEXT, #13).
8. F95 version strings are never parsed or ordered (#2). Only equality is used.
9. The daily check uses no cookie (`checker.php`); detail fetches (tags, Genre, cover, Dev status) use the stored cookie + UA (#2, #3, #8).
10. A Source is marked unavailable only after 3 consecutive misses plus a confirming detail fetch; unavailable Sources are excluded from checks until re-enabled by hand (#8).
11. Invalid cookie: `validity='invalid'`, exactly one alert per transition (`invalid_alerted_at`), detail fetches stay in `detail_fetch_queue`, new Games get `details_pending=1` (#8).
12. Rotated `Set-Cookie` values are persisted into `f95_credential.cookie_jar` (#8). Cookie is plaintext (decision, #8); the app never sends it to the downloader (#6, #13).
13. A Game tag refers to exactly one tag (F95 or custom); one row per (Game, tag); present+planned collapses to present (#9). `UNIQUE(game_id, tag_id)`.
14. Qualifier ∈ present/planned/optional; origin ∈ f95_list/genre/both/manual; verification ∈ unverified/confirmed/wrong (CONTEXT, #9).
15. Verification changes only in a Tag review (user action) except hand-picked tags on manual/itch.io Games, which start `confirmed`; add-time confirmation never verifies (CONTEXT, #9).
16. Refresh merges: user verifications and mapping overrides persist; new tags arrive `unverified` + `is_new`; tags gone from the Source are kept with `removed_at_source_at`; a planned/optional tag that appears present is promoted and flagged `promoted` (#9).
17. F95 tags absent from Genre text are `f95_only` (#9).
18. Games with Tag review `pending` or `skipped` form the tags-to-review queue; all imported F95 Games start `pending` (#9, #10).
19. A mapping fix saves a `synonym` (origin `user`); the synonym table is seeded from research `SYN` (#9).
20. Vocabulary grows automatically from every logged-in F95 tag list; custom tags exist only for phrases without an F95 slug (#9).
21. Every imported Game has `import_review=1` until the user clears it (bulk edit) (#10).
22. Import is idempotent by thread id / link (#10); `UNIQUE(kind, external_id)` is the key.
23. Detail-fetch queue: one entry per Source; routine budget 40/day, import has its own budget (#8).
24. API tokens are stored only as hashes, scoped, and revocable; a revoked token authenticates nothing (#13).
25. Download job holds an ordered mirror list of resolved URLs only; unsupported hosts stay as `manual` mirrors; no supported mirror or all failed → `needs_human` with a reason and one notification; `done` only after verify + extract (#13).
26. Versions are kept on disk per `<Game>/<version>`; the job table never deletes history while the Game exists (#13).
27. One session cookie, 30-day sliding, signed, no server-side session table; only OIDC subjects in the allowlist (module option, not DB) get a session (#12).
28. Backups use `VACUUM INTO` of a consistent DB; the schema has no state outside the one SQLite file (#12).

## Open questions for the grilling

1. **Dev status location**: on `source` (proposed, follows "fetched from its Source") or on `game` (simpler reads, one more writer rule)?
2. **Multiple Sources per Game**: allow non-primary extra Sources (proposed) or force exactly one? Does anything in the UI ever need a second?
3. **itch.io "Behind"**: with only a date when no version token exists, is last played compared against the date string, or is Behind simply not shown for itch.io Games?
4. **Alert set default** (`playing` + `on_hold`?) and whether `planned` Games ever alert.
5. **Sessions**: stateless signed cookie (revoking requires rotating the secret) vs an `scs` sqlite session table (revocable, "log out everywhere")?
6. **Dropped/cancelled download jobs**: #13 has no cancel state. Add `cancelled`, or just delete the job?
7. **History retention**: prune `check_result`, `notification`, finished jobs after N days, or keep forever (single user, small)?
8. **Imported rows' `played_on`**: also allow the user to backfill a date on an `imported` row, or is Play log strictly immutable including that?
9. **Planned/optional verification**: can a user ever confirm/mark wrong a planned or optional tag outside the review (e.g. hand-edit), or only after promotion?
10. **Cover**: store the URL only (hotlink F95 image, may rot or need cookie) or cache the file in the state dir (adds files to backup)?
