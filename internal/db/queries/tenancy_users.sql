-- name: GetOrganizationByID :one
SELECT * FROM organizations WHERE id = $1;

-- name: UpdateOrganization :one
UPDATE organizations SET name = $2 WHERE id = $1 RETURNING *;

-- name: ListMembers :many
SELECT u.id AS user_id, u.email, u.display_name, m.role, u.disabled_at, m.created_at
FROM org_memberships m JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1 ORDER BY m.created_at;

-- name: UpdateMembershipRole :execrows
-- Sets user_id's role in org_id, but refuses (0 rows affected, mapped to
-- ErrNotFound by the caller) to move a member away from 'owner' when doing
-- so would leave the organization with no owner. The last-owner guard used
-- to be a separate CountOwners call sandwiched between a read and this
-- write — three round trips wide open to a race where two concurrent
-- demotions of two different owners each observe "two owners" and both
-- commit, leaving zero. Folding the count into this statement's own WHERE
-- closes that: the nested "FOR UPDATE" subquery takes a row lock on every
-- current owner of the org before counting them, so two such statements
-- running concurrently serialize against each other (the second blocks
-- until the first commits, then re-reads the now-current owner count)
-- instead of both reading the pre-update count. count(*) cannot itself
-- carry FOR UPDATE (Postgres rejects locking clauses on aggregates), hence
-- the extra wrapping subquery. DeleteMembership below uses the identical
-- pattern.
UPDATE org_memberships AS m
SET role = $3
WHERE m.org_id = $1 AND m.user_id = $2
  AND (
    m.role != 'owner'
    -- $3's type must be pinned to org_role explicitly here: left as a bare
    -- comparison against the unknown-typed literal 'owner', Postgres
    -- resolves the parameter's type as text (its default for "unknown"),
    -- which then conflicts with the org_role SET target above and the
    -- whole statement fails to prepare ("column role is of type org_role
    -- but expression is of type text").
    OR $3 = 'owner'::org_role
    OR (SELECT count(*) FROM (
          SELECT 1 FROM org_memberships AS om WHERE om.org_id = $1 AND om.role = 'owner' FOR UPDATE
        ) AS locked_owners) > 1
  );

-- name: DeleteMembership :execrows
-- Removes user_id from org_id, refusing (0 rows affected) to remove the
-- organization's last owner. Same atomic FOR-UPDATE-locked guard as
-- UpdateMembershipRole above.
DELETE FROM org_memberships AS m
WHERE m.org_id = $1 AND m.user_id = $2
  AND (
    m.role != 'owner'
    OR (SELECT count(*) FROM (
          SELECT 1 FROM org_memberships AS om WHERE om.org_id = $1 AND om.role = 'owner' FOR UPDATE
        ) AS locked_owners) > 1
  );

-- name: CountOwners :one
SELECT count(*) FROM org_memberships WHERE org_id = $1 AND role = 'owner';

-- name: InsertInvite :exec
INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: PeekInvite :one
-- Reads the invite TakeInvite would take, without taking it: AcceptInvite
-- decides first whether this caller may accept it at all (backlog 7.10), so
-- a refused attempt never burns the invitation. The issuer condition is
-- the same as in TakeInvite.
SELECT i.* FROM invites AS i
WHERE i.token_hash = sqlc.arg(token_hash) AND i.accepted_at IS NULL AND i.expires_at > sqlc.arg(now)::timestamptz
  AND EXISTS (
      SELECT 1 FROM org_memberships AS m JOIN users AS u ON u.id = m.user_id
      WHERE m.org_id = i.org_id AND m.user_id = i.invited_by
        AND m.role::text = ANY(sqlc.arg(issuer_roles)::text[]) AND u.disabled_at IS NULL
  );

-- name: TakeInvite :one
-- Atomically finds an invite that is unaccepted and unexpired as of now and
-- marks it accepted in the same statement, so two concurrent requests
-- racing on the same token can never both succeed: the UPDATE's WHERE
-- clause re-checks accepted_at IS NULL, and Postgres serializes concurrent
-- updates to the same row, so only the first commits and the second finds
-- no matching row (mapped to ErrNotFound by the caller) rather than both
-- reading "unaccepted" before either writes.
-- An invitation carries its issuer's authority to add members, so it is
-- good only while the issuer still holds it (backlog 7.29): an active
-- account with one of issuer_roles in the invite's organization. Removing,
-- demoting or deactivating the issuer voids every invitation they sent
-- that is still open. The accept path runs with no organization bound,
-- so the RLS bypass lets this subquery see the memberships.
UPDATE invites AS i SET accepted_at = sqlc.arg(now)::timestamptz
WHERE i.token_hash = sqlc.arg(token_hash) AND i.accepted_at IS NULL AND i.expires_at > sqlc.arg(now)::timestamptz
  AND EXISTS (
      SELECT 1 FROM org_memberships AS m JOIN users AS u ON u.id = m.user_id
      WHERE m.org_id = i.org_id AND m.user_id = i.invited_by
        AND m.role::text = ANY(sqlc.arg(issuer_roles)::text[]) AND u.disabled_at IS NULL
  )
RETURNING i.*;

-- name: SetUserPasswordAndName :exec
UPDATE users SET password_hash = $2, display_name = $3 WHERE id = $1;

-- name: DisableUser :execrows
-- Disables id, but refuses (0 rows affected) to disable the instance's last
-- active instance administrator: setup is gated on "no instance
-- administrator exists", so losing the last one would leave the instance
-- unrecoverable without editing the database. The guard is atomic in the
-- same way UpdateMembershipRole's last-owner guard is: the FOR UPDATE
-- subquery locks every active administrator's row before counting them, so
-- two administrators deactivating each other concurrently serialize, and
-- the second sees the first's change instead of both reading "two active".
UPDATE users AS u SET disabled_at = $2
WHERE u.id = $1
  AND (
    NOT u.instance_admin
    OR u.disabled_at IS NOT NULL
    OR (SELECT count(*) FROM (
          SELECT 1 FROM users AS a WHERE a.instance_admin AND a.disabled_at IS NULL FOR UPDATE
        ) AS locked_admins) > 1
  );

-- name: RevokeSessionsForUser :exec
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeTokensForUser :exec
UPDATE api_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ListPendingInvites :many
-- The open invitations of one organization: those TakeInvite would still
-- take, issuer condition included (backlog 7.29), newest first.
SELECT i.id, i.email, i.role, i.invited_by, u.display_name AS invited_by_name, i.expires_at, i.created_at
FROM invites AS i JOIN users AS u ON u.id = i.invited_by
WHERE i.org_id = sqlc.arg(org_id) AND i.accepted_at IS NULL AND i.expires_at > sqlc.arg(now)::timestamptz
  AND EXISTS (
      SELECT 1 FROM org_memberships AS m JOIN users AS iu ON iu.id = m.user_id
      WHERE m.org_id = i.org_id AND m.user_id = i.invited_by
        AND m.role::text = ANY(sqlc.arg(issuer_roles)::text[]) AND iu.disabled_at IS NULL
  )
ORDER BY i.created_at DESC, i.id;

-- name: RevokeInvite :execrows
-- Withdraws an open invitation of org_id (backlog 7.29); an accepted one
-- is history and stays.
DELETE FROM invites WHERE id = $1 AND org_id = $2 AND accepted_at IS NULL;

-- name: DeleteInvite :exec
-- Removes an invitation that was never delivered: tenancy.Service.Invite
-- inserts the row before sending the mail that carries its link, and
-- deletes it again when that delivery fails, so a failed invitation leaves
-- no orphaned row behind.
DELETE FROM invites WHERE id = $1 AND org_id = $2;
