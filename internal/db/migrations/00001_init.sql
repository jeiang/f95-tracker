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
  download_job_id  INTEGER REFERENCES download_job(id) ON DELETE SET NULL,
  UNIQUE (notification_id, check_result_id)
) STRICT;

-- ============ Seeds ============

INSERT INTO settings(id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%SZ','now'));

INSERT INTO tag(kind, slug, label) VALUES
  ('f95', '2d-game', '2d game'),
  ('f95', '2dcg', '2dcg'),
  ('f95', '3d-game', '3d game'),
  ('f95', '3dcg', '3dcg'),
  ('f95', 'adventure', 'adventure'),
  ('f95', 'ahegao', 'ahegao'),
  ('f95', 'ai-cg', 'ai cg'),
  ('f95', 'anal-sex', 'anal sex'),
  ('f95', 'animated', 'animated'),
  ('f95', 'asset-addon', 'asset addon'),
  ('f95', 'asset-ai-shoujo', 'asset ai shoujo'),
  ('f95', 'asset-animal', 'asset animal'),
  ('f95', 'asset-animation', 'asset animation'),
  ('f95', 'asset-audio', 'asset audio'),
  ('f95', 'asset-bundle', 'asset bundle'),
  ('f95', 'asset-character', 'asset character'),
  ('f95', 'asset-clothing', 'asset clothing'),
  ('f95', 'asset-daz-gen1', 'asset daz gen1'),
  ('f95', 'asset-daz-gen2', 'asset daz gen2'),
  ('f95', 'asset-daz-gen3', 'asset daz gen3'),
  ('f95', 'asset-daz-gen8', 'asset daz gen8'),
  ('f95', 'asset-daz-gen81', 'asset daz gen81'),
  ('f95', 'asset-daz-gen9', 'asset daz gen9'),
  ('f95', 'asset-daz-m4', 'asset daz m4'),
  ('f95', 'asset-daz-v4', 'asset daz v4'),
  ('f95', 'asset-environment', 'asset environment'),
  ('f95', 'asset-expression', 'asset expression'),
  ('f95', 'asset-female', 'asset female'),
  ('f95', 'asset-hair', 'asset hair'),
  ('f95', 'asset-hdri', 'asset hdri'),
  ('f95', 'asset-honey-select', 'asset honey select'),
  ('f95', 'asset-honey-select2', 'asset honey select2'),
  ('f95', 'asset-koikatu', 'asset koikatu'),
  ('f95', 'asset-light', 'asset light'),
  ('f95', 'asset-male', 'asset male'),
  ('f95', 'asset-morph', 'asset morph'),
  ('f95', 'asset-nonbinary', 'asset nonbinary'),
  ('f95', 'asset-playhome', 'asset playhome'),
  ('f95', 'asset-plugin', 'asset plugin'),
  ('f95', 'asset-pose', 'asset pose'),
  ('f95', 'asset-prop', 'asset prop'),
  ('f95', 'asset-scene', 'asset scene'),
  ('f95', 'asset-script', 'asset script'),
  ('f95', 'asset-shader', 'asset shader'),
  ('f95', 'asset-texture', 'asset texture'),
  ('f95', 'asset-utility', 'asset utility'),
  ('f95', 'asset-vehicle', 'asset vehicle'),
  ('f95', 'bdsm', 'bdsm'),
  ('f95', 'bestiality', 'bestiality'),
  ('f95', 'big-ass', 'big ass'),
  ('f95', 'big-tits', 'big tits'),
  ('f95', 'blackmail', 'blackmail'),
  ('f95', 'bukkake', 'bukkake'),
  ('f95', 'censored', 'censored'),
  ('f95', 'character-creation', 'character creation'),
  ('f95', 'cheating', 'cheating'),
  ('f95', 'combat', 'combat'),
  ('f95', 'corruption', 'corruption'),
  ('f95', 'cosplay', 'cosplay'),
  ('f95', 'creampie', 'creampie'),
  ('f95', 'dating-sim', 'dating sim'),
  ('f95', 'dilf', 'dilf'),
  ('f95', 'drugs', 'drugs'),
  ('f95', 'dystopian-setting', 'dystopian setting'),
  ('f95', 'exhibitionism', 'exhibitionism'),
  ('f95', 'fantasy', 'fantasy'),
  ('f95', 'female-protagonist', 'female protagonist'),
  ('f95', 'femaledomination', 'femaledomination'),
  ('f95', 'footjob', 'footjob'),
  ('f95', 'furry', 'furry'),
  ('f95', 'futa-trans', 'futa trans'),
  ('f95', 'futa-trans-protagonist', 'futa trans protagonist'),
  ('f95', 'gay', 'gay'),
  ('f95', 'graphic-violence', 'graphic violence'),
  ('f95', 'groping', 'groping'),
  ('f95', 'group-sex', 'group sex'),
  ('f95', 'handjob', 'handjob'),
  ('f95', 'harem', 'harem'),
  ('f95', 'horror', 'horror'),
  ('f95', 'humiliation', 'humiliation'),
  ('f95', 'humor', 'humor'),
  ('f95', 'incest', 'incest'),
  ('f95', 'internal-view', 'internal view'),
  ('f95', 'interracial', 'interracial'),
  ('f95', 'japanese-game', 'japanese game'),
  ('f95', 'kinetic-novel', 'kinetic novel'),
  ('f95', 'lactation', 'lactation'),
  ('f95', 'lesbian', 'lesbian'),
  ('f95', 'loli', 'loli'),
  ('f95', 'male-protagonist', 'male protagonist'),
  ('f95', 'maledomination', 'maledomination'),
  ('f95', 'management', 'management'),
  ('f95', 'masturbation', 'masturbation'),
  ('f95', 'milf', 'milf'),
  ('f95', 'mind-control', 'mind control'),
  ('f95', 'mobile-game', 'mobile game'),
  ('f95', 'monster', 'monster'),
  ('f95', 'monster-girl', 'monster girl'),
  ('f95', 'multiple-endings', 'multiple endings'),
  ('f95', 'multiple-penetration', 'multiple penetration'),
  ('f95', 'multiple-protagonist', 'multiple protagonist'),
  ('f95', 'necrophilia', 'necrophilia'),
  ('f95', 'no-sexual-content', 'no sexual content'),
  ('f95', 'ntr', 'ntr'),
  ('f95', 'oral-sex', 'oral sex'),
  ('f95', 'paranormal', 'paranormal'),
  ('f95', 'parody', 'parody'),
  ('f95', 'platformer', 'platformer'),
  ('f95', 'point-click', 'point click'),
  ('f95', 'possession', 'possession'),
  ('f95', 'pov', 'pov'),
  ('f95', 'pregnancy', 'pregnancy'),
  ('f95', 'prostitution', 'prostitution'),
  ('f95', 'puzzle', 'puzzle'),
  ('f95', 'rape', 'rape'),
  ('f95', 'real-porn', 'real porn'),
  ('f95', 'religion', 'religion'),
  ('f95', 'romance', 'romance'),
  ('f95', 'rpg', 'rpg'),
  ('f95', 'sandbox', 'sandbox'),
  ('f95', 'scat', 'scat'),
  ('f95', 'school-setting', 'school setting'),
  ('f95', 'sci-fi', 'sci fi'),
  ('f95', 'sex-toys', 'sex toys'),
  ('f95', 'sexual-harassment', 'sexual harassment'),
  ('f95', 'shooter', 'shooter'),
  ('f95', 'shota', 'shota'),
  ('f95', 'side-scroller', 'side scroller'),
  ('f95', 'simulator', 'simulator'),
  ('f95', 'sissification', 'sissification'),
  ('f95', 'slave', 'slave'),
  ('f95', 'sleep-sex', 'sleep sex'),
  ('f95', 'spanking', 'spanking'),
  ('f95', 'strategy', 'strategy'),
  ('f95', 'stripping', 'stripping'),
  ('f95', 'superpowers', 'superpowers'),
  ('f95', 'swinging', 'swinging'),
  ('f95', 'teasing', 'teasing'),
  ('f95', 'tentacles', 'tentacles'),
  ('f95', 'text-based', 'text based'),
  ('f95', 'titfuck', 'titfuck'),
  ('f95', 'trainer', 'trainer'),
  ('f95', 'transformation', 'transformation'),
  ('f95', 'trap', 'trap'),
  ('f95', 'turn-based-combat', 'turn based combat'),
  ('f95', 'twins', 'twins'),
  ('f95', 'urination', 'urination'),
  ('f95', 'vaginal-sex', 'vaginal sex'),
  ('f95', 'virgin', 'virgin'),
  ('f95', 'virtual-reality', 'virtual reality'),
  ('f95', 'voiced', 'voiced'),
  ('f95', 'vore', 'vore'),
  ('f95', 'voyeurism', 'voyeurism');

INSERT INTO synonym(phrase_key, tag_id, origin, created_at)
SELECT v.column1, t.id, 'seed', strftime('%Y-%m-%dT%H:%M:%SZ','now')
FROM (VALUES
  ('futa', 'futa-trans'),
  ('futanari', 'futa-trans'),
  ('futatrans', 'futa-trans'),
  ('3dgc', '3dcg'),
  ('haren', 'harem'),
  ('oral', 'oral-sex'),
  ('blowjob', 'oral-sex'),
  ('anal', 'anal-sex'),
  ('vaginal', 'vaginal-sex'),
  ('femdom', 'femaledomination'),
  ('netori', 'ntr'),
  ('netorare', 'ntr'),
  ('scifi', 'sci-fi'),
  ('toys', 'sex-toys'),
  ('largebreasts', 'big-tits'),
  ('boobjob', 'titfuck'),
  ('boobsjob', 'titfuck'),
  ('titjob', 'titfuck'),
  ('superpower', 'superpowers'),
  ('possesion', 'possession'),
  ('violence', 'graphic-violence'),
  ('group', 'group-sex'),
  ('yuri', 'lesbian'),
  ('datingsim', 'dating-sim'),
  ('pointandclick', 'point-click'),
  ('femaleprotagonist', 'female-protagonist'),
  ('futaprotagonist', 'futa-trans-protagonist')
) AS v JOIN tag t ON t.kind = 'f95' AND t.slug = v.column2;

-- +goose Down
DROP VIEW game_behind;
DROP VIEW game_last_played;
DROP TABLE download_job_transition;
DROP TABLE download_mirror;
DROP TABLE download_job;
DROP TABLE notification_item;
DROP TABLE notification;
DROP TABLE detail_fetch_queue;
DROP TABLE check_result;
DROP TABLE check_run;
DROP TABLE sessions;
DROP TABLE api_token;
DROP TABLE f95_credential;
DROP TABLE alert_play_status;
DROP TABLE settings;
DROP TABLE tag_review;
DROP TABLE game_tag;
DROP TABLE synonym;
DROP TABLE tag;
DROP TABLE play_log;
DROP TABLE source;
DROP TABLE game;
