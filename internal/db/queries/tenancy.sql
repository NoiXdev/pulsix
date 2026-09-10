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

-- name: AddMembership :exec
INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $2, $3);

-- name: GetMembership :one
SELECT * FROM org_memberships WHERE org_id = $1 AND user_id = $2;

-- name: ListMembershipsForUser :many
SELECT m.*, o.slug, o.name FROM org_memberships m JOIN organizations o ON o.id = m.org_id WHERE m.user_id = $1;
