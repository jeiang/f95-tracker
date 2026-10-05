-- review queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetTagReview :one
SELECT * FROM tag_review WHERE game_id = ?;
