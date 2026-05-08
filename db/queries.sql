-- name: ListRecipes :many
SELECT * FROM recipes ORDER BY title;

-- name: ListPublicRecipes :many
SELECT * FROM recipes WHERE private = 0 ORDER BY title;

-- name: GetRecipeBySlug :one
SELECT * FROM recipes WHERE slug = ? LIMIT 1;

-- name: GetRecipeByID :one
SELECT * FROM recipes WHERE id = ? LIMIT 1;

-- name: CreateRecipe :one
INSERT INTO recipes (slug, title, source, instructions, private) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: UpdateRecipe :exec
UPDATE recipes SET title = ?, slug = ?, source = ?, instructions = ?, private = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?;

-- name: DeleteRecipe :exec
DELETE FROM recipes WHERE slug = ?;

-- name: CreateIngredientGroup :one
INSERT INTO ingredient_groups (recipe_id, name, sort_order) VALUES (?, ?, ?) RETURNING *;

-- name: ListIngredientGroupsByRecipeID :many
SELECT * FROM ingredient_groups WHERE recipe_id = ? ORDER BY sort_order;

-- name: DeleteIngredientGroupsByRecipeID :exec
DELETE FROM ingredient_groups WHERE recipe_id = ?;

-- name: ListIngredientsByRecipeID :many
SELECT * FROM ingredients WHERE recipe_id = ? ORDER BY sort_order;

-- name: CreateIngredient :exec
INSERT INTO ingredients (recipe_id, group_id, quantity, unit, name, sort_order) VALUES (?, ?, ?, ?, ?, ?);

-- name: DeleteIngredientsByRecipeID :exec
DELETE FROM ingredients WHERE recipe_id = ?;

-- name: ListAllTags :many
SELECT * FROM tags ORDER BY name;

-- name: GetOrCreateTag :one
INSERT INTO tags (name) VALUES (?) ON CONFLICT (name) DO UPDATE SET name = excluded.name RETURNING *;

-- name: AddRecipeTag :exec
INSERT OR IGNORE INTO recipe_tags (recipe_id, tag_id) VALUES (?, ?);

-- name: DeleteRecipeTagsByRecipeID :exec
DELETE FROM recipe_tags WHERE recipe_id = ?;

-- name: DeleteOrphanedTags :exec
DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM recipe_tags);

-- name: ListTagsByRecipeID :many
SELECT t.id, t.name FROM tags t
JOIN recipe_tags rt ON rt.tag_id = t.id
WHERE rt.recipe_id = ?
ORDER BY t.name;

-- name: GetShareLinkByRecipeID :one
SELECT id, recipe_id, token, created_at FROM share_links WHERE recipe_id = ? LIMIT 1;

-- name: CreateShareLink :one
INSERT INTO share_links (recipe_id, token) VALUES (?, ?) RETURNING id, recipe_id, token, created_at;

-- name: UpsertUser :one
INSERT INTO users (username, name) VALUES (?, ?)
ON CONFLICT (username) DO UPDATE SET
    name = excluded.name,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: CreateRecipeLog :one
INSERT INTO recipe_logs (user_id, recipe_id, cooked_on) VALUES (?, ?, ?) RETURNING *;

-- name: GetRecipeLog :one
SELECT * FROM recipe_logs WHERE id = ? LIMIT 1;

-- name: UpdateRecipeLogDate :exec
UPDATE recipe_logs SET cooked_on = ? WHERE id = ?;

-- name: DeleteRecipeLog :exec
DELETE FROM recipe_logs WHERE id = ?;

-- name: ListRecipeLogsByRecipeID :many
SELECT l.id, l.user_id, l.recipe_id, l.cooked_on, l.created_at, u.username
FROM recipe_logs l
JOIN users u ON u.id = l.user_id
WHERE l.recipe_id = ?
ORDER BY l.cooked_on DESC, l.created_at DESC;

-- name: ListRecipeLogsByUserID :many
SELECT l.id, l.user_id, l.recipe_id, l.cooked_on, l.created_at,
       r.slug AS recipe_slug, r.title AS recipe_title
FROM recipe_logs l
JOIN recipes r ON r.id = l.recipe_id
WHERE l.user_id = ?
ORDER BY l.cooked_on DESC, l.created_at DESC;

-- name: ListRecentRecipeLogs :many
SELECT l.id, l.user_id, l.recipe_id, l.cooked_on, l.created_at, u.username
FROM recipe_logs l
JOIN users u ON u.id = l.user_id
WHERE l.cooked_on >= ?
ORDER BY l.cooked_on DESC;
