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
