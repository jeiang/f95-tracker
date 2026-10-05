-- sources queries. The owning backlog item adds its queries to this file (see docs/spec/backlog.md).

-- name: InsertSource :one
INSERT INTO source (game_id, kind, is_primary, external_id, url, latest_version, change_key, dev_status, thread_updated_at, genre_text, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPrimarySource :one
SELECT * FROM source WHERE game_id = ? AND is_primary = 1;

-- name: GetSource :one
SELECT * FROM source WHERE id = ?;

-- name: ListSourcesByGame :many
SELECT * FROM source WHERE game_id = ? ORDER BY is_primary DESC, id;

-- name: FindSourceByExternal :one
SELECT * FROM source WHERE kind = ? AND external_id = ?;

-- name: FindSourceByURL :one
SELECT * FROM source WHERE kind = ? AND url = ?;

-- Detail-fetch writer; NULL arguments keep the stored value. dev_status is only passed for F95 Sources.
-- name: ApplySourceDetail :exec
UPDATE source SET
  latest_version    = COALESCE(sqlc.narg(latest_version), latest_version),
  change_key        = COALESCE(sqlc.narg(change_key), change_key),
  dev_status        = COALESCE(sqlc.narg(dev_status), dev_status),
  thread_updated_at = COALESCE(sqlc.narg(thread_updated_at), thread_updated_at),
  genre_text        = COALESCE(sqlc.narg(genre_text), genre_text),
  last_detail_at    = sqlc.arg(detail_at),
  details_pending   = sqlc.arg(details_pending)
WHERE id = sqlc.arg(id);

-- name: SetSourceBaseline :exec
UPDATE source SET latest_version = ?, change_key = ? WHERE id = ?;

-- name: SetSourceDevStatus :exec
UPDATE source SET dev_status = ? WHERE id = ?;

-- Link-only form required by the non-primary CHECK.
-- name: DemoteSource :exec
UPDATE source SET is_primary = 0, latest_version = NULL, change_key = NULL, dev_status = NULL,
  thread_updated_at = NULL, genre_text = NULL, last_checked_at = NULL, last_detail_at = NULL, miss_count = 0,
  details_pending = 0, unavailable_at = NULL, unavailable_reason = NULL, checks_enabled = 1
WHERE id = ?;

-- name: PromoteSource :exec
UPDATE source SET is_primary = 1 WHERE id = ?;

-- name: DeleteDetailFetchQueue :exec
DELETE FROM detail_fetch_queue WHERE source_id = ?;

-- name: ReenableSource :exec
UPDATE source SET checks_enabled = 1, unavailable_at = NULL, unavailable_reason = NULL, miss_count = 0 WHERE id = ?;

-- name: SetSourceDetailsPending :exec
UPDATE source SET details_pending = ? WHERE id = ?;

-- name: EnqueueDetailFetch :exec
-- At most one pending fetch per Source; an existing row keeps its original reason and budget.
INSERT INTO detail_fetch_queue (source_id, reason, budget, enqueued_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (source_id) DO NOTHING;
