-- downloads queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetDownloadJob :one
SELECT * FROM download_job WHERE id = ?;
