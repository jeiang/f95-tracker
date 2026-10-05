-- notify queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetNotification :one
SELECT * FROM notification WHERE id = ?;

-- name: InsertNotification :one
INSERT INTO notification (kind, run_id, title, body, created_at)
VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: InsertNotificationItem :exec
INSERT INTO notification_item (notification_id, game_id, check_result_id, download_job_id)
VALUES (?, ?, ?, ?);

-- name: MarkNotificationSent :exec
UPDATE notification SET sent_at = ?, error = NULL WHERE id = ?;

-- name: MarkNotificationFailed :exec
UPDATE notification SET error = ? WHERE id = ?;

-- name: ListUnsentImmediateNotifications :many
SELECT * FROM notification WHERE sent_at IS NULL AND kind <> 'digest' ORDER BY id;

-- name: ClaimCookieInvalidAlert :execrows
UPDATE f95_credential SET invalid_alerted_at = ?
WHERE id = 1 AND validity = 'invalid' AND invalid_alerted_at IS NULL;

-- name: GetSourceNotifyInfo :one
SELECT game_id, unavailable_at FROM source WHERE id = ?;

-- name: SourceUnavailableAlerted :one
SELECT EXISTS(
  SELECT 1 FROM notification_item i JOIN notification n ON n.id = i.notification_id
  WHERE n.kind = 'source_unavailable' AND i.game_id = ? AND n.created_at >= ?
) AS alerted;
