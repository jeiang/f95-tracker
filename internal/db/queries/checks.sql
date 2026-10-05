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
