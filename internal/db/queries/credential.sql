-- f95_credential queries (single row, id = 1). Owned by the F95 client item.

-- name: GetF95Credential :one
SELECT * FROM f95_credential WHERE id = 1;

-- name: ReplaceF95Credential :exec
INSERT INTO f95_credential (id, cookie_jar, user_agent, validity, tfa_trust_expires_at, updated_at)
VALUES (1, ?, ?, 'unknown', ?, ?)
ON CONFLICT (id) DO UPDATE SET
  cookie_jar = excluded.cookie_jar,
  user_agent = excluded.user_agent,
  validity = 'unknown',
  validated_at = NULL,
  tfa_trust_expires_at = excluded.tfa_trust_expires_at,
  updated_at = excluded.updated_at;

-- name: UpdateF95CredentialJar :exec
UPDATE f95_credential SET cookie_jar = ?, updated_at = ? WHERE id = 1;

-- name: MarkF95CredentialValid :exec
UPDATE f95_credential
SET validity = 'valid', validated_at = ?1, invalid_alerted_at = NULL, updated_at = ?1
WHERE id = 1;

-- name: MarkF95CredentialInvalid :execrows
UPDATE f95_credential SET validity = 'invalid', updated_at = ?
WHERE id = 1 AND validity <> 'invalid';

-- name: MarkF95CredentialAlerted :exec
UPDATE f95_credential SET invalid_alerted_at = ?1, updated_at = ?1 WHERE id = 1;
