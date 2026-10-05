-- checks queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetLatestCheckRun :one
SELECT * FROM check_run ORDER BY id DESC LIMIT 1;
