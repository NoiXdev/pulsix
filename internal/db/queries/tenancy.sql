-- name: CreateOrganization :one
INSERT INTO organizations (id, slug, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations WHERE slug = $1;

-- name: CreateUser :one
INSERT INTO users (id, email, display_name, password_hash) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: SetUserLocale :execrows
-- A NULL locale clears the user's own choice (the instance default applies
-- again). :execrows so an id that matches nothing is told apart from a
-- successful write (backlog 1.41).
UPDATE users SET locale = sqlc.narg(locale) WHERE id = sqlc.arg(id);

-- name: AddMembership :exec
-- ON CONFLICT DO NOTHING mirrors the (org_id, user_id) primary key: accepting
-- an invitation for an address that is already a member must join
-- gracefully (the existing membership and role are left exactly as they
-- were), not surface a unique-violation as an unmapped 500 after
-- TakeInvite has already burned the invite (Finding 5, Task 10 fix round
-- 1). The in-memory fake in service_test.go mirrors this exact semantic.
INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $2, $3)
ON CONFLICT (org_id, user_id) DO NOTHING;

-- name: GetMembership :one
SELECT * FROM org_memberships WHERE org_id = $1 AND user_id = $2;

-- name: ListMembershipsForUser :many
SELECT m.*, o.slug, o.name FROM org_memberships m JOIN organizations o ON o.id = m.org_id WHERE m.user_id = $1;
