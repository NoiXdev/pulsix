-- name: ListPasskeys :many
SELECT * FROM passkeys WHERE user_id = $1 ORDER BY created_at;

-- name: InsertPasskey :exec
INSERT INTO passkeys (id, user_id, name, credential_id, public_key, attestation, transports, sign_count, backup_eligible)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpdatePasskeyCounter :exec
UPDATE passkeys SET sign_count = $2, last_used_at = now() WHERE credential_id = $1;

-- name: GetPasskeyByCredentialID :one
SELECT * FROM passkeys WHERE credential_id = $1;

-- name: DeletePasskey :execrows
DELETE FROM passkeys WHERE id = $1 AND user_id = $2;

-- name: PutCeremony :exec
INSERT INTO webauthn_ceremonies (id, kind, user_id, data, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: TakeCeremony :one
DELETE FROM webauthn_ceremonies WHERE id = $1 AND expires_at > now() RETURNING kind, user_id, data;
