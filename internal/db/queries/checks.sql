-- checks queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetLatestCheckRun :one
SELECT * FROM check_run ORDER BY id DESC LIMIT 1;

-- name: InsertCheckRun :one
INSERT INTO check_run (kind, started_at) VALUES (?, ?) RETURNING *;

-- name: FinishCheckRun :exec
UPDATE check_run SET status = ?, finished_at = ?, f95_stopped = ? WHERE id = ?;

-- name: InsertCheckResult :one
INSERT INTO check_result (run_id, source_id, step, outcome, old_key, new_key, http_status, attempts, error, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- itch.io answer: the version token is NULL when the page has none (date-only Source).
-- name: ApplyItchPage :exec
UPDATE source SET latest_version = sqlc.narg(latest_version), change_key = sqlc.arg(change_key),
  thread_updated_at = sqlc.narg(thread_updated_at), last_checked_at = sqlc.arg(at), last_detail_at = sqlc.arg(at),
  miss_count = 0, details_pending = 0
WHERE id = sqlc.arg(id);

-- name: DisableSourceChecks :exec
UPDATE source SET checks_enabled = 0 WHERE id = ?;

-- Checkable primary Sources of one kind with what the daily run needs (R-UPD-10).
-- name: ListCheckableSources :many
SELECT s.id, s.game_id, s.external_id, s.url, s.latest_version, s.change_key, s.thread_updated_at, s.miss_count,
  g.name AS game_name,
  CAST(g.play_status IN (SELECT play_status FROM alert_play_status) AS INTEGER) AS alerting
FROM source s JOIN game g ON g.id = s.game_id
WHERE s.is_primary = 1 AND s.checks_enabled = 1 AND s.unavailable_at IS NULL AND s.kind = ?
ORDER BY s.id;

-- name: MarkSourceChecked :exec
UPDATE source SET last_checked_at = ?, miss_count = 0 WHERE id = ?;

-- name: ResetSourceMisses :exec
UPDATE source SET miss_count = 0 WHERE id = ?;

-- name: IncrementSourceMiss :one
UPDATE source SET miss_count = miss_count + 1 WHERE id = ? RETURNING miss_count;

-- name: MarkSourceUnavailable :exec
UPDATE source SET unavailable_at = ?, unavailable_reason = ? WHERE id = ?;

-- Weekly rolling refresh candidates: never or long ago fetched, not finished, not already queued.
-- name: ListWeeklyCandidates :many
SELECT s.id FROM source s JOIN game g ON g.id = s.game_id
WHERE s.kind = 'f95_thread' AND s.is_primary = 1 AND s.checks_enabled = 1 AND s.unavailable_at IS NULL
  AND g.play_status <> 'finished'
  AND (s.last_detail_at IS NULL OR s.last_detail_at < ?)
  AND NOT EXISTS (SELECT 1 FROM detail_fetch_queue d WHERE d.source_id = s.id)
ORDER BY s.last_detail_at IS NOT NULL, s.last_detail_at, s.id
LIMIT ?;

-- name: ListRoutineQueue :many
SELECT d.source_id FROM detail_fetch_queue d JOIN source s ON s.id = d.source_id
WHERE d.budget = 'routine' AND s.kind = 'f95_thread' AND s.is_primary = 1 AND s.unavailable_at IS NULL
ORDER BY d.enqueued_at, d.source_id;

-- Routine detail fetches already made by daily runs in [from, to).
-- name: CountDailyDetailResults :one
SELECT COUNT(*) FROM check_result r JOIN check_run u ON u.id = r.run_id
WHERE u.kind = 'daily' AND r.step = 'detail' AND r.at >= ? AND r.at < ?;
