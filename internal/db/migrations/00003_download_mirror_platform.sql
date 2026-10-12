-- +goose Up
-- The worker API reports mac and android mirrors (data-model 'other' lost that). SQLite cannot
-- alter a CHECK, so the table is rebuilt; M6 was unused until now, so it holds no rows.
DROP TABLE download_mirror;
CREATE TABLE download_mirror (
  id        INTEGER PRIMARY KEY,
  job_id    INTEGER NOT NULL REFERENCES download_job(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  host      TEXT NOT NULL,
  url       TEXT NOT NULL,
  platform  TEXT CHECK (platform IN ('linux','win_linux','win','mac','android','other')),
  supported INTEGER NOT NULL CHECK (supported IN (0,1)),
  state     TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','active','failed','done','manual')),
  error     TEXT,
  attempts  INTEGER NOT NULL DEFAULT 0,
  UNIQUE (job_id, position),
  CHECK (supported = 1 OR state = 'manual')
) STRICT;

-- +goose Down
DROP TABLE download_mirror;
CREATE TABLE download_mirror (
  id        INTEGER PRIMARY KEY,
  job_id    INTEGER NOT NULL REFERENCES download_job(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  host      TEXT NOT NULL,
  url       TEXT NOT NULL,
  platform  TEXT CHECK (platform IN ('linux','win_linux','win','other')),
  supported INTEGER NOT NULL CHECK (supported IN (0,1)),
  state     TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','active','failed','done','manual')),
  error     TEXT,
  attempts  INTEGER NOT NULL DEFAULT 0,
  UNIQUE (job_id, position),
  CHECK (supported = 1 OR state = 'manual')
) STRICT;
