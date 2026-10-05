-- notify queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetNotification :one
SELECT * FROM notification WHERE id = ?;
