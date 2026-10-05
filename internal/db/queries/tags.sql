-- tags queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: CountTagsByKind :one
SELECT COUNT(*) FROM tag WHERE kind = ?;

-- name: CountSynonymsByOrigin :one
SELECT COUNT(*) FROM synonym WHERE origin = ?;
