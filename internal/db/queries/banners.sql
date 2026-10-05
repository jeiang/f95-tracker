-- Read-only queries for the list page, entry strips, tab visibility and global banners.

-- name: CountImportReviewGames :one
SELECT COUNT(*) FROM game WHERE import_review = 1;

-- name: CountTagReviewQueue :one
SELECT COUNT(*) FROM tag_review WHERE state IN ('pending', 'skipped');

-- name: GetF95CredentialHealth :one
SELECT validity, tfa_trust_expires_at FROM f95_credential WHERE id = 1;

-- Tags the list filter can offer: those currently counted by the filter (not wrong, not removed at Source).
-- name: ListFilterTags :many
SELECT t.id, t.slug, t.label, COUNT(DISTINCT gt.game_id) AS games
  FROM tag t
  JOIN game_tag gt ON gt.tag_id = t.id AND gt.verification <> 'wrong' AND gt.removed_at_source_at IS NULL
 GROUP BY t.id
 ORDER BY games DESC, t.label COLLATE NOCASE, t.id;
