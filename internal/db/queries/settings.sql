-- name: InstanceAdminExists :one
-- The gate setup.Service.Complete checks before doing anything else: the
-- wizard is allowed to run only while no instance administrator exists yet.
SELECT EXISTS (SELECT 1 FROM users WHERE instance_admin);

-- name: InstanceAdminMFAEnrolled :one
-- Reports whether the (single) instance administrator has confirmed a TOTP
-- secret or registered a passkey, so setup.Service.Status can tell the SPA
-- whether to keep insisting on second-factor enrolment after the wizard
-- itself has already completed.
SELECT EXISTS (
  SELECT 1 FROM users u
  WHERE u.instance_admin
    AND (
      EXISTS (SELECT 1 FROM totp_secrets t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
      OR EXISTS (SELECT 1 FROM passkeys p WHERE p.user_id = u.id)
    )
);

-- name: CreateInstanceAdmin :one
INSERT INTO users (id, email, display_name, password_hash, instance_admin)
VALUES ($1, $2, $3, $4, true)
RETURNING *;

-- name: UpsertInstanceSetting :exec
INSERT INTO instance_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: GetInstanceSetting :one
SELECT * FROM instance_settings WHERE key = $1;
