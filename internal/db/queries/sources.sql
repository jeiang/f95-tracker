-- sources queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: InsertSource :one
INSERT INTO source (game_id, kind, is_primary, external_id, url, latest_version, change_key, dev_status, thread_updated_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPrimarySource :one
SELECT * FROM source WHERE game_id = ? AND is_primary = 1;
