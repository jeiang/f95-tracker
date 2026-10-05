-- playlog queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: InsertPlayLog :one
INSERT INTO play_log (game_id, version, played_on, origin, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;
