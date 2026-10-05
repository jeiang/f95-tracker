-- games queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetGame :one
SELECT * FROM game WHERE id = ?;

-- name: InsertGame :one
INSERT INTO game (name, play_status, rating_x2, import_review, added_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListBehindGameIDs :many
SELECT game_id FROM game_behind ORDER BY game_id;
