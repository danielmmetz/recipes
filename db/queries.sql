-- name: ListRecipes :many
SELECT * FROM recipes ORDER BY title;

-- name: GetRecipeBySlug :one
SELECT * FROM recipes WHERE slug = ? LIMIT 1;

-- name: CreateRecipe :one
INSERT INTO recipes (slug, title, instructions) VALUES (?, ?, ?) RETURNING *;

-- name: UpdateRecipe :exec
UPDATE recipes SET title = ?, slug = ?, instructions = ?, updated_at = CURRENT_TIMESTAMP WHERE slug = ?;

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
