-- auth queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: CountActiveAPITokens :one
SELECT COUNT(*) FROM api_token WHERE revoked_at IS NULL;

-- name: FindSession :one
SELECT data FROM sessions WHERE token = ? AND expiry > ?;

-- name: UpsertSession :exec
INSERT INTO sessions (token, data, expiry) VALUES (?, ?, ?)
ON CONFLICT (token) DO UPDATE SET data = excluded.data, expiry = excluded.expiry;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expiry <= ?;

-- name: CountSessions :one
SELECT COUNT(*) FROM sessions;

-- name: InsertAPIToken :one
INSERT INTO api_token (name, scope, token_hash, created_at) VALUES (?, ?, ?, ?) RETURNING *;

-- name: GetAPITokenByHash :one
SELECT * FROM api_token WHERE token_hash = ? AND revoked_at IS NULL;

-- name: ListAPITokens :many
SELECT * FROM api_token WHERE revoked_at IS NULL ORDER BY id;

-- name: RevokeAPIToken :execrows
UPDATE api_token SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_token SET last_used_at = ?1 WHERE id = ?2 AND (last_used_at IS NULL OR last_used_at <= ?3);
