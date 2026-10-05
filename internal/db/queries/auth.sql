-- auth queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: CountActiveAPITokens :one
SELECT COUNT(*) FROM api_token WHERE revoked_at IS NULL;
