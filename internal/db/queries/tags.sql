-- tags queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: CountTagsByKind :one
SELECT COUNT(*) FROM tag WHERE kind = ?;

-- name: CountSynonymsByOrigin :one
SELECT COUNT(*) FROM synonym WHERE origin = ?;

-- name: UpsertTag :one
INSERT INTO tag (kind, slug, label) VALUES (?, ?, ?)
ON CONFLICT (kind, slug) DO UPDATE SET kind = excluded.kind
RETURNING *;

-- name: GetTag :one
SELECT * FROM tag WHERE id = ?;

-- name: GetTagByKindSlug :one
SELECT * FROM tag WHERE kind = ? AND slug = ?;

-- name: ListTags :many
SELECT * FROM tag ORDER BY kind, label COLLATE NOCASE, id;

-- name: ListTagsByKind :many
SELECT * FROM tag WHERE kind = ? ORDER BY slug;

-- name: ListSynonymRows :many
SELECT s.id, s.phrase_key, s.tag_id, s.origin, s.created_at,
       t.kind AS tag_kind, t.slug AS tag_slug, t.label AS tag_label
  FROM synonym s JOIN tag t ON t.id = s.tag_id
 WHERE sqlc.arg(filter) = ''
    OR instr(s.phrase_key, sqlc.arg(filter)) > 0
    OR instr(lower(t.slug), sqlc.arg(filter)) > 0
    OR instr(lower(t.label), sqlc.arg(filter)) > 0
 ORDER BY s.phrase_key;

-- name: GetSynonym :one
SELECT * FROM synonym WHERE id = ?;

-- name: GetSynonymByKey :one
SELECT * FROM synonym WHERE phrase_key = ?;

-- name: InsertSynonym :one
INSERT INTO synonym (phrase_key, tag_id, origin, created_at) VALUES (?, ?, ?, ?)
RETURNING *;

-- name: UpdateSynonym :one
UPDATE synonym SET phrase_key = ?, tag_id = ?, origin = 'user' WHERE id = ?
RETURNING *;

-- name: DeleteSynonym :exec
DELETE FROM synonym WHERE id = ?;

-- name: ListGameTags :many
SELECT gt.*, t.kind AS tag_kind, t.slug AS tag_slug, t.label AS tag_label
  FROM game_tag gt JOIN tag t ON t.id = gt.tag_id
 WHERE gt.game_id = ?
 ORDER BY gt.id;

-- name: GetGameTag :one
SELECT * FROM game_tag WHERE id = ?;

-- name: GetGameTagByTag :one
SELECT * FROM game_tag WHERE game_id = ? AND tag_id = ?;

-- name: InsertGameTag :one
INSERT INTO game_tag (game_id, tag_id, origin, qualifier, verification, modifier_note, source_phrase,
                      mapping_override, is_new, promoted, removed_at_source_at, f95_only, verified_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateGameTag :exec
UPDATE game_tag SET tag_id = ?, origin = ?, qualifier = ?, verification = ?, modifier_note = ?, source_phrase = ?,
       mapping_override = ?, is_new = ?, promoted = ?, removed_at_source_at = ?, f95_only = ?, verified_at = ?
 WHERE id = ?;

-- name: DeleteGameTag :exec
DELETE FROM game_tag WHERE id = ?;

-- name: ClearGameTagFlags :exec
UPDATE game_tag SET is_new = 0, promoted = 0 WHERE game_id = ?;

-- Rows a synonym change may re-point (R-TAG-10, INV-19).
-- name: ListReapplyCandidates :many
SELECT * FROM game_tag
 WHERE verification = 'unverified' AND mapping_override = 0
   AND source_phrase IS NOT NULL AND removed_at_source_at IS NULL
 ORDER BY game_id, id;

-- name: CountPresentTags :one
SELECT COUNT(*) FROM game_tag
 WHERE game_id = ? AND qualifier = 'present' AND verification <> 'wrong' AND removed_at_source_at IS NULL;

-- name: CountChangedTags :one
SELECT COUNT(*) FROM game_tag
 WHERE game_id = ? AND (is_new = 1 OR promoted = 1 OR removed_at_source_at IS NOT NULL);

-- name: CountUnreviewedPresentTags :one
SELECT COUNT(*) FROM game_tag
 WHERE game_id = ? AND qualifier = 'present' AND removed_at_source_at IS NULL AND verification = 'unverified';
