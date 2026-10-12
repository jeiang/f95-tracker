-- downloads queries (R-DL). Job state changes go through internal/downloads only.

-- name: GetDownloadJob :one
SELECT * FROM download_job WHERE id = ?;

-- name: GetOpenDownloadJob :one
SELECT * FROM download_job
WHERE game_id = ? AND target_version = ? AND state NOT IN ('done','cancelled');

-- name: InsertDownloadJob :one
INSERT INTO download_job (game_id, source_id, target_version, trigger, state, created_at, updated_at)
VALUES (?, ?, ?, ?, 'awaiting_links', ?, ?) RETURNING *;

-- name: SetDownloadJobState :exec
UPDATE download_job SET state = ?, needs_human_reason = ?, updated_at = ? WHERE id = ?;

-- name: SetDownloadJobProgress :exec
UPDATE download_job SET
  active_mirror_id = COALESCE(sqlc.narg('active_mirror_id'), active_mirror_id),
  bytes_done = COALESCE(sqlc.narg('bytes_done'), bytes_done),
  bytes_total = COALESCE(sqlc.narg('bytes_total'), bytes_total),
  result_path = COALESCE(sqlc.narg('result_path'), result_path),
  updated_at = sqlc.arg('updated_at')
WHERE id = sqlc.arg('id');

-- name: InsertDownloadTransition :exec
INSERT INTO download_job_transition (job_id, from_state, to_state, actor, detail, at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListDownloadTransitions :many
SELECT * FROM download_job_transition WHERE job_id = ? ORDER BY id;

-- name: DownloadJobRow :one
SELECT sqlc.embed(j), g.name AS game_name, g.platform_pref AS game_platform_pref
FROM download_job j JOIN game g ON g.id = j.game_id
WHERE j.id = ?;

-- name: ListDownloadJobRows :many
SELECT sqlc.embed(j), g.name AS game_name, g.platform_pref AS game_platform_pref
FROM download_job j JOIN game g ON g.id = j.game_id
WHERE j.state IN (sqlc.slice('states'))
ORDER BY j.id;

-- name: ListRecentDownloadJobRows :many
SELECT sqlc.embed(j), g.name AS game_name, g.platform_pref AS game_platform_pref
FROM download_job j JOIN game g ON g.id = j.game_id
WHERE j.state NOT IN ('done','cancelled') OR j.updated_at >= ?
ORDER BY (j.state IN ('done','cancelled')), j.id DESC;

-- name: ListCancelledDownloadJobIDs :many
SELECT id FROM download_job WHERE state = 'cancelled' AND updated_at >= ? ORDER BY id;

-- name: ListDownloadMirrors :many
SELECT * FROM download_mirror WHERE job_id = ? ORDER BY position;

-- name: ListDownloadMirrorsForJobs :many
SELECT * FROM download_mirror WHERE job_id IN (sqlc.slice('job_ids')) ORDER BY job_id, position;

-- name: GetDownloadMirror :one
SELECT * FROM download_mirror WHERE id = ?;

-- name: InsertDownloadMirror :exec
INSERT INTO download_mirror (job_id, position, host, url, platform, supported, state)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: DeleteDownloadMirrors :exec
DELETE FROM download_mirror WHERE job_id = ?;

-- name: SetDownloadMirrorState :exec
UPDATE download_mirror SET state = ?, error = ?, attempts = attempts + ? WHERE id = ?;

-- name: ResetFailedDownloadMirrors :exec
UPDATE download_mirror SET state = 'pending', error = NULL WHERE job_id = ? AND state = 'failed';

-- name: CountUnfailedSupportedMirrors :one
SELECT COUNT(*) FROM download_mirror WHERE job_id = ? AND supported = 1 AND state <> 'failed';

-- name: CountSupportedMirrors :one
SELECT COUNT(*) FROM download_mirror WHERE job_id = ? AND supported = 1;

-- name: FindThreadGame :one
SELECT s.game_id, s.latest_version, s.id AS source_id
FROM source s WHERE s.kind = 'f95_thread' AND s.external_id = ?;

-- name: ListOpenDownloadJobsForGame :many
SELECT * FROM download_job WHERE game_id = ? AND state NOT IN ('done','cancelled') ORDER BY id DESC;

-- name: DownloadNeedsHumanPushed :one
SELECT EXISTS(
  SELECT 1 FROM notification_item i JOIN notification n ON n.id = i.notification_id
  WHERE n.kind = 'download_needs_human' AND i.download_job_id = ?
) AS pushed;

-- Jobs the digest lists (R-NOTIF-6): awaiting links and needs human always (they wait on the user),
-- done only when unreported. is_new = no digest has covered the job since it entered its current state.
-- name: ListDigestDownloadJobs :many
SELECT j.id, j.game_id, g.name AS game_name, j.target_version, j.state, j.needs_human_reason,
  CAST(NOT EXISTS(
    SELECT 1 FROM notification_item i JOIN notification n ON n.id = i.notification_id
    WHERE i.download_job_id = j.id AND n.kind = 'digest'
      AND n.created_at >= COALESCE((SELECT MAX(t.at) FROM download_job_transition t
                                    WHERE t.job_id = j.id AND t.to_state = j.state), j.created_at)
  ) AS INTEGER) AS is_new
FROM download_job j JOIN game g ON g.id = j.game_id
WHERE j.state IN ('awaiting_links','needs_human','done')
ORDER BY j.id;
