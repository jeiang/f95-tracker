-- games queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetGame :one
SELECT * FROM game WHERE id = ?;

-- name: InsertGame :one
INSERT INTO game (name, play_status, rating_x2, import_review, added_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListBehindGameIDs :many
SELECT game_id FROM game_behind ORDER BY game_id;

-- name: DeleteGame :exec
DELETE FROM game WHERE id = ?;

-- name: UpdateGamePlayStatus :exec
UPDATE game SET play_status = ?, updated_at = ? WHERE id = ?;

-- name: UpdateGameRating :exec
UPDATE game SET rating_x2 = ?, updated_at = ? WHERE id = ?;

-- name: UpdateGamePlatformPref :exec
UPDATE game SET platform_pref = ?, updated_at = ? WHERE id = ?;

-- name: ClearGameImportReview :exec
UPDATE game SET import_review = 0, updated_at = ? WHERE id = ?;

-- name: UpdateGameName :exec
UPDATE game SET name = ?, updated_at = ? WHERE id = ?;

-- name: UpdateGameCover :exec
UPDATE game SET cover_path = ?, cover_source_url = ?, cover_fetched_at = ?, updated_at = ? WHERE id = ?;

-- name: CountGamesByPlayStatus :many
SELECT play_status, COUNT(*) AS n FROM game GROUP BY play_status;

-- Behind and Update badges: alert-set Games only (R-UPD-9). Update = an 'update' check_result on the
-- primary Source newer than the newest Play log entry was created.
-- name: GetGameFlags :one
SELECT
  CAST(g.play_status IN (SELECT play_status FROM alert_play_status) AS INTEGER) AS alerting,
  CAST(g.play_status IN (SELECT play_status FROM alert_play_status)
       AND EXISTS (SELECT 1 FROM game_behind b WHERE b.game_id = g.id) AS INTEGER) AS behind,
  CAST(g.play_status IN (SELECT play_status FROM alert_play_status)
       AND EXISTS (SELECT 1 FROM check_result cr JOIN source s ON s.id = cr.source_id
                   WHERE s.game_id = g.id AND s.is_primary = 1 AND cr.outcome = 'update'
                     AND cr.at > COALESCE((SELECT MAX(p.created_at) FROM play_log p WHERE p.game_id = g.id), '')) AS INTEGER) AS has_update
FROM game g WHERE g.id = ?;

-- name: ListGames :many
WITH base AS (
  SELECT g.id, g.name, g.play_status, g.rating_x2, g.cover_path, g.platform_pref, g.import_review,
         g.added_at, g.updated_at,
         s.id AS source_id, s.kind AS source_kind, s.url AS source_url, s.latest_version, s.dev_status,
         s.thread_updated_at, s.last_checked_at, s.details_pending, s.unavailable_at,
         lp.version AS last_played_version, lp.played_on AS last_played_on,
         CAST(CASE WHEN EXISTS (SELECT 1 FROM game_tag gt WHERE gt.game_id = g.id AND gt.qualifier = 'present' AND gt.verification <> 'wrong' AND gt.removed_at_source_at IS NULL) THEN COALESCE(tr.state, '') ELSE '' END AS TEXT) AS review_state,
         CAST(sqlc.arg(sort) AS TEXT) AS sort_mode, CAST(sqlc.arg(dir) AS TEXT) AS sort_dir,
         CAST(g.play_status IN (SELECT play_status FROM alert_play_status)
              AND EXISTS (SELECT 1 FROM game_behind b WHERE b.game_id = g.id) AS INTEGER) AS behind,
         CAST(g.play_status IN (SELECT play_status FROM alert_play_status)
              AND EXISTS (SELECT 1 FROM check_result cr
                          WHERE cr.source_id = s.id AND cr.outcome = 'update'
                            AND cr.at > COALESCE((SELECT MAX(p.created_at) FROM play_log p WHERE p.game_id = g.id), '')) AS INTEGER) AS has_update
  FROM game g
  JOIN source s ON s.game_id = g.id AND s.is_primary = 1
  LEFT JOIN game_last_played lp ON lp.game_id = g.id
  LEFT JOIN tag_review tr ON tr.game_id = g.id
)
SELECT * FROM base
WHERE instr(lower(name), lower(sqlc.arg(name_text))) > 0
  AND (CAST(sqlc.narg(play_status) AS TEXT) IS NULL OR play_status = CAST(sqlc.narg(play_status) AS TEXT))
  AND (CAST(sqlc.narg(dev_status) AS TEXT) IS NULL OR dev_status = CAST(sqlc.narg(dev_status) AS TEXT))
  AND (CAST(sqlc.narg(min_rating_x2) AS INTEGER) IS NULL OR rating_x2 >= CAST(sqlc.narg(min_rating_x2) AS INTEGER))
  AND (CAST(sqlc.arg(behind_only) AS INTEGER) = 0 OR behind = 1)
  AND (CAST(sqlc.arg(updates_only) AS INTEGER) = 0 OR has_update = 1)
  AND NOT EXISTS (SELECT 1 FROM json_each(CAST(sqlc.arg(include_tags) AS TEXT)) j
                  WHERE NOT EXISTS (SELECT 1 FROM game_tag gt
                                    WHERE gt.game_id = base.id AND gt.tag_id = j.value
                                      AND gt.verification <> 'wrong' AND gt.removed_at_source_at IS NULL
                                      AND (CAST(sqlc.arg(all_qualifiers) AS INTEGER) = 1 OR gt.qualifier = 'present')))
  AND NOT EXISTS (SELECT 1 FROM json_each(CAST(sqlc.arg(exclude_tags) AS TEXT)) j
                  WHERE EXISTS (SELECT 1 FROM game_tag gt
                                WHERE gt.game_id = base.id AND gt.tag_id = j.value
                                  AND gt.verification <> 'wrong' AND gt.removed_at_source_at IS NULL
                                  AND (CAST(sqlc.arg(all_qualifiers) AS INTEGER) = 1 OR gt.qualifier = 'present')))
ORDER BY
  CASE WHEN sort_mode = 'default' THEN behind OR has_update END DESC,
  CASE WHEN sort_mode = 'default' THEN thread_updated_at END DESC,
  CASE WHEN sort_mode = 'name' AND sort_dir = 'asc' THEN lower(name) END ASC,
  CASE WHEN sort_mode = 'name' AND sort_dir = 'desc' THEN lower(name) END DESC,
  CASE WHEN sort_mode = 'rating' THEN rating_x2 IS NULL END ASC,
  CASE WHEN sort_mode = 'rating' AND sort_dir = 'asc' THEN rating_x2 END ASC,
  CASE WHEN sort_mode = 'rating' AND sort_dir = 'desc' THEN rating_x2 END DESC,
  CASE WHEN sort_mode = 'play_status' AND sort_dir = 'asc' THEN
    CASE play_status WHEN 'playing' THEN 0 WHEN 'on_hold' THEN 1 WHEN 'planned' THEN 2 WHEN 'finished' THEN 3 ELSE 4 END END ASC,
  CASE WHEN sort_mode = 'play_status' AND sort_dir = 'desc' THEN
    CASE play_status WHEN 'playing' THEN 0 WHEN 'on_hold' THEN 1 WHEN 'planned' THEN 2 WHEN 'finished' THEN 3 ELSE 4 END END DESC,
  CASE WHEN sort_mode = 'last_played' THEN last_played_on IS NULL END ASC,
  CASE WHEN sort_mode = 'last_played' AND sort_dir = 'asc' THEN last_played_on END ASC,
  CASE WHEN sort_mode = 'last_played' AND sort_dir = 'desc' THEN last_played_on END DESC,
  CASE WHEN sort_mode = 'added' AND sort_dir = 'asc' THEN added_at END ASC,
  CASE WHEN sort_mode = 'added' AND sort_dir = 'desc' THEN added_at END DESC,
  id DESC;
