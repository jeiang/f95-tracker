-- Read query for the import review list (R-CSV-10).

-- name: ListImportReviewGames :many
SELECT g.id, g.name, g.play_status, s.dev_status,
       (SELECT COUNT(*) FROM play_log p WHERE p.game_id = g.id) AS play_log_count,
       lp.version AS last_played_version
  FROM game g
  LEFT JOIN source s ON s.game_id = g.id AND s.is_primary = 1
  LEFT JOIN game_last_played lp ON lp.game_id = g.id
 WHERE g.import_review = 1
 ORDER BY g.name COLLATE NOCASE, g.id;
