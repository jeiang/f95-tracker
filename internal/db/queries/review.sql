-- review queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: GetTagReview :one
SELECT * FROM tag_review WHERE game_id = ?;

-- name: EnsureTagReview :exec
INSERT INTO tag_review (game_id, state, updated_at) VALUES (?, 'pending', ?)
ON CONFLICT (game_id) DO NOTHING;

-- Moves the review to pending or skipped, keeping the last completed review's markers.
-- name: SetTagReviewState :exec
INSERT INTO tag_review (game_id, state, updated_at) VALUES (?, ?, ?)
ON CONFLICT (game_id) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at;

-- name: CompleteTagReview :exec
INSERT INTO tag_review (game_id, state, last_reviewed_play_log_id, reviewed_at, updated_at)
VALUES (sqlc.arg(game_id), 'done', sqlc.narg(play_log_id), sqlc.arg(at), sqlc.arg(at))
ON CONFLICT (game_id) DO UPDATE SET state = 'done', last_reviewed_play_log_id = excluded.last_reviewed_play_log_id,
  reviewed_at = excluded.reviewed_at, updated_at = excluded.updated_at;

-- Queue = pending/skipped reviews of Games with at least one present, non-wrong tag at the Source (an imported Game has a pending row before its tags are fetched).
-- name: ListReviewQueue :many
SELECT tr.game_id, g.name, tr.state, tr.updated_at,
       (SELECT COUNT(*) FROM game_tag gt WHERE gt.game_id = tr.game_id AND gt.qualifier = 'present'
          AND gt.verification <> 'wrong' AND gt.removed_at_source_at IS NULL) AS present_count,
       (SELECT COUNT(*) FROM game_tag gt WHERE gt.game_id = tr.game_id AND gt.qualifier = 'present'
          AND gt.verification = 'unverified' AND gt.removed_at_source_at IS NULL) AS unverified_count,
       lp.version AS last_played_version
  FROM tag_review tr
  JOIN game g ON g.id = tr.game_id
  LEFT JOIN game_last_played lp ON lp.game_id = tr.game_id
 WHERE tr.state IN ('pending', 'skipped')
   AND EXISTS (SELECT 1 FROM game_tag gt WHERE gt.game_id = tr.game_id AND gt.qualifier = 'present' AND gt.verification <> 'wrong' AND gt.removed_at_source_at IS NULL)
 ORDER BY tr.updated_at, g.name COLLATE NOCASE, g.id;
