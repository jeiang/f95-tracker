-- playlog queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: InsertPlayLog :one
INSERT INTO play_log (game_id, version, played_on, origin, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPlayLog :one
SELECT * FROM play_log WHERE id = ?;

-- name: ListPlayLog :many
SELECT * FROM play_log WHERE game_id = ? ORDER BY played_on IS NULL, played_on DESC, id DESC;

-- name: CountUserPlayLog :one
SELECT COUNT(*) FROM play_log WHERE game_id = ? AND origin = 'user';

-- name: UpdatePlayLog :one
UPDATE play_log SET version = ?, played_on = ? WHERE id = ? RETURNING *;

-- name: DeletePlayLog :exec
DELETE FROM play_log WHERE id = ?;

-- name: ClearTagReviewPlayLogRef :exec
UPDATE tag_review SET last_reviewed_play_log_id = NULL WHERE last_reviewed_play_log_id = ?;

-- name: GetGameLastPlayed :one
SELECT * FROM game_last_played WHERE game_id = ?;
