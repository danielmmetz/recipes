-- name: ListRecipes :many
SELECT * FROM recipes ORDER BY title;

-- name: GetRecipeBySlug :one
SELECT * FROM recipes WHERE slug = ? LIMIT 1;

-- name: CreateRecipe :one
INSERT INTO recipes (slug, title, source, instructions) VALUES (?, ?, ?, ?) RETURNING *;

-- name: UpdateRecipe :exec
UPDATE recipes SET title = ?, slug = ?, source = ?, instructions = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?;

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
