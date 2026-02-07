package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielmmetz/recipes/db/generated"
	"github.com/yuin/goldmark"
)

type server struct {
	queries *generated.Queries
	db      *sql.DB
	pages   map[string]*template.Template
	partial *template.Template
}

// templateIngredient extends the generated Ingredient with a GroupName and UnitGroups for template rendering.
type templateIngredient struct {
	generated.Ingredient
	GroupName  string
	UnitGroups []UnitGroup
}

// ingredientGroupData holds a group and its ingredients for template rendering.
type ingredientGroupData struct {
	Name        string
	Ingredients []templateIngredient
}

// indexRecipe extends Recipe with its tags for display on the index page.
type indexRecipe struct {
	generated.Recipe
	Tags []generated.Tag
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	allTags, err := s.queries.ListAllTags(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	selectedTagNames := r.URL.Query()["tag"]

	recipes, err := s.queries.ListRecipes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build index recipes with tags, filtering if tags are selected
	selectedSet := map[string]bool{}
	for _, t := range selectedTagNames {
		selectedSet[t] = true
	}

	var indexRecipes []indexRecipe
	for _, recipe := range recipes {
		tags, err := s.queries.ListTagsByRecipeID(r.Context(), recipe.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if len(selectedTagNames) > 0 {
			// Recipe must have ALL selected tags
			recipeTagSet := map[string]bool{}
			for _, t := range tags {
				recipeTagSet[t.Name] = true
			}
			match := true
			for _, st := range selectedTagNames {
				if !recipeTagSet[st] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}

		indexRecipes = append(indexRecipes, indexRecipe{Recipe: recipe, Tags: tags})
	}

	s.render(w, "index.html", map[string]any{
		"Recipes":      indexRecipes,
		"AllTags":       allTags,
		"SelectedTags":  selectedSet,
	})
}

func (s *server) handleNewRecipe(w http.ResponseWriter, r *http.Request) {
	allTags, err := s.queries.ListAllTags(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "form.html", map[string]any{
		"IsEdit":                false,
		"UngroupedIngredients": []templateIngredient{{UnitGroups: StandardUnitGroups}},
		"Groups":               []ingredientGroupData{},
		"AllTags":               allTags,
		"RecipeTags":            []generated.Tag{},
	})
}

func (s *server) handleCreateRecipe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	source := r.FormValue("source")
	instructions := r.FormValue("instructions")
	slug := slugify(title)

	recipe, err := s.queries.CreateRecipe(r.Context(), generated.CreateRecipeParams{
		Slug:         slug,
		Title:        title,
		Source:       source,
		Instructions: instructions,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.createIngredients(r, recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.saveTags(r, recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/recipes/"+recipe.Slug, http.StatusSeeOther)
}

func (s *server) handleViewRecipe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	recipe, err := s.queries.GetRecipeBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Recipe not found", http.StatusNotFound)
		return
	}
	ingredients, err := s.queries.ListIngredientsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups, err := s.queries.ListIngredientGroupsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ungrouped, groupedData := buildGroupedView(ingredients, groups)

	tags, err := s.queries.ListTagsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var mdBuf bytes.Buffer
	if err := goldmark.Convert([]byte(recipe.Instructions), &mdBuf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "view.html", map[string]any{
		"Recipe":                recipe,
		"UngroupedIngredients": ungrouped,
		"Groups":               groupedData,
		"HasIngredients":       len(ingredients) > 0,
		"InstructionsHTML":     template.HTML(mdBuf.String()),
		"Tags":                 tags,
	})
}

func (s *server) handleEditRecipe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	recipe, err := s.queries.GetRecipeBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Recipe not found", http.StatusNotFound)
		return
	}
	ingredients, err := s.queries.ListIngredientsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups, err := s.queries.ListIngredientGroupsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ungrouped, groupedData := buildGroupedView(ingredients, groups)

	// Ensure at least one empty row for ungrouped if there are none at all
	if len(ungrouped) == 0 && len(groupedData) == 0 {
		ungrouped = []templateIngredient{{UnitGroups: StandardUnitGroups}}
	}

	allTags, err := s.queries.ListAllTags(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recipeTags, err := s.queries.ListTagsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.render(w, "form.html", map[string]any{
		"IsEdit":                true,
		"Recipe":                recipe,
		"UngroupedIngredients": ungrouped,
		"Groups":               groupedData,
		"AllTags":               allTags,
		"RecipeTags":            recipeTags,
	})
}

func (s *server) handleUpdateRecipe(w http.ResponseWriter, r *http.Request) {
	oldSlug := r.PathValue("slug")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	source := r.FormValue("source")
	instructions := r.FormValue("instructions")
	newSlug := slugify(title)

	recipe, err := s.queries.GetRecipeBySlug(r.Context(), oldSlug)
	if err != nil {
		http.Error(w, "Recipe not found", http.StatusNotFound)
		return
	}

	err = s.queries.UpdateRecipe(r.Context(), generated.UpdateRecipeParams{
		Title:        title,
		Slug:         newSlug,
		Source:       source,
		Instructions: instructions,
		Slug_2:       oldSlug,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete old ingredients and groups, then re-create
	if err := s.queries.DeleteIngredientsByRecipeID(r.Context(), recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.queries.DeleteIngredientGroupsByRecipeID(r.Context(), recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.createIngredients(r, recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.saveTags(r, recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/recipes/"+newSlug)
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := s.queries.DeleteRecipe(r.Context(), slug); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.queries.DeleteOrphanedTags(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleIngredientRow(w http.ResponseWriter, r *http.Request) {
	s.partial.ExecuteTemplate(w, "ingredient_row", templateIngredient{UnitGroups: StandardUnitGroups})
}

func (s *server) saveTags(r *http.Request, recipeID int64) error {
	if err := s.queries.DeleteRecipeTagsByRecipeID(r.Context(), recipeID); err != nil {
		return fmt.Errorf("deleting existing recipe tags: %w", err)
	}
	tagsRaw := r.FormValue("tags")
	if tagsRaw == "" {
		if err := s.queries.DeleteOrphanedTags(r.Context()); err != nil {
			return fmt.Errorf("deleting orphaned tags: %w", err)
		}
		return nil
	}
	for _, name := range strings.Split(tagsRaw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		tag, err := s.queries.GetOrCreateTag(r.Context(), name)
		if err != nil {
			return fmt.Errorf("creating tag %q: %w", name, err)
		}
		if err := s.queries.AddRecipeTag(r.Context(), generated.AddRecipeTagParams{
			RecipeID: recipeID,
			TagID:    tag.ID,
		}); err != nil {
			return fmt.Errorf("adding tag %q to recipe: %w", name, err)
		}
	}
	if err := s.queries.DeleteOrphanedTags(r.Context()); err != nil {
		return fmt.Errorf("deleting orphaned tags: %w", err)
	}
	return nil
}

func (s *server) createIngredients(r *http.Request, recipeID int64) error {
	names := r.Form["name[]"]
	quantities := r.Form["quantity[]"]
	units := r.Form["unit[]"]
	groupNames := r.Form["ingredient_group[]"]

	// First pass: collect unique group names in order and create them
	groupIDByName := map[string]sql.NullInt64{}
	groupOrder := 0
	for _, gn := range groupNames {
		if gn == "" {
			continue
		}
		if _, exists := groupIDByName[gn]; exists {
			continue
		}
		group, err := s.queries.CreateIngredientGroup(r.Context(), generated.CreateIngredientGroupParams{
			RecipeID:  recipeID,
			Name:      gn,
			SortOrder: int64(groupOrder),
		})
		if err != nil {
			return fmt.Errorf("creating ingredient group %q: %w", gn, err)
		}
		groupIDByName[gn] = sql.NullInt64{Int64: group.ID, Valid: true}
		groupOrder++
	}

	// Second pass: create ingredients
	for i, name := range names {
		if name == "" {
			continue
		}
		var qty sql.NullFloat64
		if i < len(quantities) && quantities[i] != "" {
			f, err := strconv.ParseFloat(quantities[i], 64)
			if err == nil {
				qty = sql.NullFloat64{Float64: f, Valid: true}
			}
		}
		var unit sql.NullString
		if i < len(units) && units[i] != "" {
			if !IsValidUnit(units[i]) {
				return fmt.Errorf("invalid unit %q for ingredient %q", units[i], name)
			}
			unit = sql.NullString{String: units[i], Valid: true}
		}
		var groupID sql.NullInt64
		if i < len(groupNames) && groupNames[i] != "" {
			groupID = groupIDByName[groupNames[i]]
		}
		if err := s.queries.CreateIngredient(r.Context(), generated.CreateIngredientParams{
			RecipeID:  recipeID,
			GroupID:   groupID,
			Quantity:  qty,
			Unit:      unit,
			Name:      name,
			SortOrder: int64(i),
		}); err != nil {
			return fmt.Errorf("creating ingredient %q: %w", name, err)
		}
	}
	return nil
}

// buildGroupedView organizes ingredients into ungrouped and grouped slices for templates.
func buildGroupedView(ingredients []generated.Ingredient, groups []generated.IngredientGroup) ([]templateIngredient, []ingredientGroupData) {
	groupByID := map[int64]string{}
	for _, g := range groups {
		groupByID[g.ID] = g.Name
	}

	var ungrouped []templateIngredient
	grouped := map[int64][]templateIngredient{}
	for _, ing := range ingredients {
		ti := templateIngredient{Ingredient: ing, UnitGroups: StandardUnitGroups}
		if ing.GroupID.Valid {
			ti.GroupName = groupByID[ing.GroupID.Int64]
			grouped[ing.GroupID.Int64] = append(grouped[ing.GroupID.Int64], ti)
		} else {
			ungrouped = append(ungrouped, ti)
		}
	}

	var groupedData []ingredientGroupData
	for _, g := range groups {
		groupedData = append(groupedData, ingredientGroupData{
			Name:        g.Name,
			Ingredients: grouped[g.ID],
		})
	}

	return ungrouped, groupedData
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}
