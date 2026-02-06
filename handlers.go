package main

import (
	"bytes"
	"database/sql"
	"html/template"
	"net/http"
	"strconv"

	"github.com/danielmmetz/recipes/db/generated"
)

type server struct {
	queries *generated.Queries
	db      *sql.DB
	pages   map[string]*template.Template
	partial *template.Template
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	recipes, err := s.queries.ListRecipes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "index.html", map[string]any{"Recipes": recipes})
}

func (s *server) handleNewRecipe(w http.ResponseWriter, r *http.Request) {
	s.render(w, "form.html", map[string]any{
		"IsEdit":      false,
		"Ingredients": []generated.Ingredient{{}},
	})
}

func (s *server) handleCreateRecipe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	instructions := r.FormValue("instructions")
	slug := slugify(title)

	recipe, err := s.queries.CreateRecipe(r.Context(), generated.CreateRecipeParams{
		Slug:         slug,
		Title:        title,
		Instructions: instructions,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.createIngredients(w, r, recipe.ID)
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
	s.render(w, "view.html", map[string]any{
		"Recipe":      recipe,
		"Ingredients": ingredients,
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
	s.render(w, "form.html", map[string]any{
		"IsEdit":      true,
		"Recipe":      recipe,
		"Ingredients": ingredients,
	})
}

func (s *server) handleUpdateRecipe(w http.ResponseWriter, r *http.Request) {
	oldSlug := r.PathValue("slug")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
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
		Instructions: instructions,
		Slug_2:       oldSlug,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete old ingredients and re-create
	if err := s.queries.DeleteIngredientsByRecipeID(r.Context(), recipe.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.createIngredients(w, r, recipe.ID)

	w.Header().Set("HX-Redirect", "/recipes/"+newSlug)
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := s.queries.DeleteRecipe(r.Context(), slug); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleIngredientRow(w http.ResponseWriter, r *http.Request) {
	s.partial.ExecuteTemplate(w, "ingredient_row", generated.Ingredient{})
}

func (s *server) createIngredients(w http.ResponseWriter, r *http.Request, recipeID int64) {
	names := r.Form["name[]"]
	quantities := r.Form["quantity[]"]
	units := r.Form["unit[]"]

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
			unit = sql.NullString{String: units[i], Valid: true}
		}
		s.queries.CreateIngredient(r.Context(), generated.CreateIngredientParams{
			RecipeID:  recipeID,
			Quantity:  qty,
			Unit:      unit,
			Name:      name,
			SortOrder: int64(i),
		})
	}
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
