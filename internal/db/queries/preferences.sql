-- name: GetUserPreferences :one
SELECT density, page_size, lists FROM user_preferences WHERE user_id = $1;

-- name: UpsertUserPreferences :one
INSERT INTO user_preferences (user_id, density, page_size, lists)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE
SET density = EXCLUDED.density, page_size = EXCLUDED.page_size, lists = EXCLUDED.lists, updated_at = now()
RETURNING density, page_size, lists;
