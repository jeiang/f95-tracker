-- settings queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetSettings :one
SELECT * FROM settings WHERE id = 1;

-- name: ListAlertPlayStatuses :many
SELECT play_status FROM alert_play_status ORDER BY play_status;
