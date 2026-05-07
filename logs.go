package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/danielmmetz/recipes/db/generated"
)

// recipeLogRow is one log entry as shown on a recipe page.
type recipeLogRow struct {
	ID       int64
	Username string
	CookedOn string
	IsMine   bool
}

// otherUserSummary aggregates one other user's logs for a recipe.
type otherUserSummary struct {
	Username string
	Count    int
}

// logsSectionData backs the recipe_logs_section partial.
type logsSectionData struct {
	Recipe         generated.Recipe
	Auth           AuthInfo
	AuthEnabled    bool
	MyLogs         []recipeLogRow
	OtherSummaries []otherUserSummary
	OtherLogs      []recipeLogRow
	TotalLogs      int
	OthersUsers    int
	OthersLogs     int
}

func (s *server) buildLogsSection(r *http.Request, recipe generated.Recipe) (logsSectionData, error) {
	ai := authInfoFromContext(r.Context())
	rows, err := s.queries.ListRecipeLogsByRecipeID(r.Context(), recipe.ID)
	if err != nil {
		return logsSectionData{}, fmt.Errorf("listing logs: %w", err)
	}

	data := logsSectionData{
		Recipe:      recipe,
		Auth:        ai,
		AuthEnabled: s.auth.enabled(),
		TotalLogs:   len(rows),
	}

	otherCounts := map[string]int{}
	for _, row := range rows {
		entry := recipeLogRow{
			ID:       row.ID,
			Username: row.Username,
			CookedOn: row.CookedOn,
			IsMine:   ai.IsLoggedIn && row.UserID == ai.UserID,
		}
		if entry.IsMine {
			data.MyLogs = append(data.MyLogs, entry)
		} else {
			data.OtherLogs = append(data.OtherLogs, entry)
			otherCounts[row.Username]++
		}
	}

	data.OthersLogs = len(data.OtherLogs)
	data.OthersUsers = len(otherCounts)

	for u, n := range otherCounts {
		data.OtherSummaries = append(data.OtherSummaries, otherUserSummary{Username: u, Count: n})
	}
	sort.Slice(data.OtherSummaries, func(i, j int) bool {
		if data.OtherSummaries[i].Count != data.OtherSummaries[j].Count {
			return data.OtherSummaries[i].Count > data.OtherSummaries[j].Count
		}
		return data.OtherSummaries[i].Username < data.OtherSummaries[j].Username
	})

	return data, nil
}

func (s *server) renderLogsSection(w http.ResponseWriter, r *http.Request, recipe generated.Recipe) {
	data, err := s.buildLogsSection(r, recipe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.partial.ExecuteTemplate(w, "recipe_logs_section", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// validateCookedOn parses a YYYY-MM-DD date and rejects dates more than one
// day in the future (UTC), tolerating timezone slop without admitting obvious
// far-future typos.
func validateCookedOn(raw string) (string, error) {
	d, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return "", fmt.Errorf("parsing date %q: %w", raw, err)
	}
	upper := time.Now().UTC().AddDate(0, 0, 1)
	if d.After(upper) {
		return "", fmt.Errorf("date %q is too far in the future", raw)
	}
	return d.Format("2006-01-02"), nil
}

func (s *server) handleLogRecipe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	recipe, err := s.queries.GetRecipeBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Recipe not found", http.StatusNotFound)
		return
	}
	ai := authInfoFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cookedOn, err := validateCookedOn(r.FormValue("cooked_on"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.queries.CreateRecipeLog(r.Context(), generated.CreateRecipeLogParams{
		UserID:   ai.UserID,
		RecipeID: recipe.ID,
		CookedOn: cookedOn,
	}); err != nil {
		http.Error(w, fmt.Errorf("creating log: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	s.renderLogsSection(w, r, recipe)
}

func (s *server) handleEditLog(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid log id", http.StatusBadRequest)
		return
	}
	log, err := s.queries.GetRecipeLog(r.Context(), id)
	if err != nil {
		http.Error(w, "Log not found", http.StatusNotFound)
		return
	}
	ai := authInfoFromContext(r.Context())
	if log.UserID != ai.UserID && !ai.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cookedOn, err := validateCookedOn(r.FormValue("cooked_on"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if log.UserID != ai.UserID {
		s.logger.Info("admin editing another user's log", "actor", ai.Username, "log_id", log.ID, "owner_user_id", log.UserID)
	}
	if err := s.queries.UpdateRecipeLogDate(r.Context(), generated.UpdateRecipeLogDateParams{
		CookedOn: cookedOn,
		ID:       log.ID,
	}); err != nil {
		http.Error(w, fmt.Errorf("updating log: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	recipe, err := s.queries.GetRecipeByID(r.Context(), log.RecipeID)
	if err != nil {
		http.Error(w, fmt.Errorf("loading recipe: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	s.renderLogsSection(w, r, recipe)
}

func (s *server) handleDeleteLog(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid log id", http.StatusBadRequest)
		return
	}
	log, err := s.queries.GetRecipeLog(r.Context(), id)
	if err != nil {
		http.Error(w, "Log not found", http.StatusNotFound)
		return
	}
	ai := authInfoFromContext(r.Context())
	if log.UserID != ai.UserID && !ai.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if log.UserID != ai.UserID {
		s.logger.Info("admin deleting another user's log", "actor", ai.Username, "log_id", log.ID, "owner_user_id", log.UserID)
	}
	if err := s.queries.DeleteRecipeLog(r.Context(), log.ID); err != nil {
		http.Error(w, fmt.Errorf("deleting log: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	recipe, err := s.queries.GetRecipeByID(r.Context(), log.RecipeID)
	if err != nil {
		http.Error(w, fmt.Errorf("loading recipe: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	// /me/logs caller swaps a row out, but we always re-render the recipe
	// section: any client that wanted the per-recipe view stays consistent,
	// and /me/logs pages do a full reload after delete.
	s.renderLogsSection(w, r, recipe)
}

// myLogsRow is one of the user's own logs, prepared for the /me/logs page.
type myLogsRow struct {
	ID          int64
	CookedOn    string
	RecipeSlug  string
	RecipeTitle string
}

// myLogsRecipeGroup groups one user's logs for a single recipe.
type myLogsRecipeGroup struct {
	RecipeSlug  string
	RecipeTitle string
	MostRecent  string
	Count       int
	Logs        []myLogsRow
}

func (s *server) handleMyLogs(w http.ResponseWriter, r *http.Request) {
	ai := authInfoFromContext(r.Context())
	rows, err := s.queries.ListRecipeLogsByUserID(r.Context(), ai.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	view := r.URL.Query().Get("view")
	if view != "by-recipe" {
		view = "chronological"
	}

	chronological := make([]myLogsRow, 0, len(rows))
	for _, row := range rows {
		chronological = append(chronological, myLogsRow{
			ID:          row.ID,
			CookedOn:    row.CookedOn,
			RecipeSlug:  row.RecipeSlug,
			RecipeTitle: row.RecipeTitle,
		})
	}

	groupsByRecipe := map[int64]*myLogsRecipeGroup{}
	var orderedGroups []*myLogsRecipeGroup
	for _, row := range rows {
		g, ok := groupsByRecipe[row.RecipeID]
		if !ok {
			g = &myLogsRecipeGroup{
				RecipeSlug:  row.RecipeSlug,
				RecipeTitle: row.RecipeTitle,
				MostRecent:  row.CookedOn,
			}
			groupsByRecipe[row.RecipeID] = g
			orderedGroups = append(orderedGroups, g)
		}
		if row.CookedOn > g.MostRecent {
			g.MostRecent = row.CookedOn
		}
		g.Logs = append(g.Logs, myLogsRow{
			ID:          row.ID,
			CookedOn:    row.CookedOn,
			RecipeSlug:  row.RecipeSlug,
			RecipeTitle: row.RecipeTitle,
		})
		g.Count++
	}
	sort.Slice(orderedGroups, func(i, j int) bool {
		if orderedGroups[i].MostRecent != orderedGroups[j].MostRecent {
			return orderedGroups[i].MostRecent > orderedGroups[j].MostRecent
		}
		return orderedGroups[i].RecipeTitle < orderedGroups[j].RecipeTitle
	})

	s.render(w, r, "me_logs.html", map[string]any{
		"View":          view,
		"Chronological": chronological,
		"ByRecipe":      orderedGroups,
		"TotalLogs":     len(rows),
	})
}

// trendingEntry is one row of the trending list.
type trendingEntry struct {
	Recipe        generated.Recipe
	Score         float64
	LogsCounted   int
	DistinctUsers int
}

func (s *server) handleTrending(w http.ResponseWriter, r *http.Request) {
	cutoff := time.Now().UTC().AddDate(0, 0, -365).Format("2006-01-02")
	logs, err := s.queries.ListRecentRecipeLogs(r.Context(), cutoff)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type pair struct {
		userID, recipeID int64
	}
	perUserCount := map[pair]int{}
	for _, l := range logs {
		perUserCount[pair{l.UserID, l.RecipeID}]++
	}

	now := time.Now().UTC()
	const halfLifeDays = 30.0
	scores := map[int64]float64{}
	logsCounted := map[int64]int{}
	usersByRecipe := map[int64]map[int64]struct{}{}
	for _, l := range logs {
		d, err := time.Parse("2006-01-02", l.CookedOn)
		if err != nil {
			continue
		}
		days := now.Sub(d).Hours() / 24
		if days < 0 {
			days = 0
		}
		decay := math.Pow(0.5, days/halfLifeDays)
		n := perUserCount[pair{l.UserID, l.RecipeID}]
		dampener := (1 + math.Log2(float64(n))) / float64(n)
		scores[l.RecipeID] += decay * dampener
		logsCounted[l.RecipeID]++
		users, ok := usersByRecipe[l.RecipeID]
		if !ok {
			users = map[int64]struct{}{}
			usersByRecipe[l.RecipeID] = users
		}
		users[l.UserID] = struct{}{}
	}

	entries := make([]trendingEntry, 0, len(scores))
	for recipeID, score := range scores {
		recipe, err := s.queries.GetRecipeByID(r.Context(), recipeID)
		if err != nil {
			continue
		}
		entries = append(entries, trendingEntry{
			Recipe:        recipe,
			Score:         score,
			LogsCounted:   logsCounted[recipeID],
			DistinctUsers: len(usersByRecipe[recipeID]),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].Recipe.Title < entries[j].Recipe.Title
	})

	s.render(w, r, "trending.html", map[string]any{
		"Entries": entries,
	})
}
