package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
	MyLogCount     int    // = len(MyLogs)
	DistinctCooks  int    // distinct cooks across all logs (incl. self)
	LastLogRel     string // "today" / "2d ago" / "1w ago" / "" if no logs
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
	data.MyLogCount = len(data.MyLogs)
	data.DistinctCooks = data.OthersUsers
	if data.MyLogCount > 0 {
		data.DistinctCooks++
	}
	if len(rows) > 0 {
		// rows are already ordered by cooked_on DESC, created_at DESC.
		data.LastLogRel = relativeDay(rows[0].CookedOn)
	}

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

// relativeDay renders a YYYY-MM-DD calendar date as a short relative string
// ("today", "yesterday", "Nd ago", "Nw ago", "Nmo ago", "Ny ago"). Returns ""
// if the input can't be parsed.
func relativeDay(cookedOn string) string {
	d, err := time.Parse("2006-01-02", cookedOn)
	if err != nil {
		return ""
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	diff := int(today.Sub(d).Hours() / 24)
	switch {
	case diff <= 0:
		return "today"
	case diff == 1:
		return "yesterday"
	case diff < 7:
		return fmt.Sprintf("%dd ago", diff)
	case diff < 30:
		return fmt.Sprintf("%dw ago", diff/7)
	case diff < 365:
		return fmt.Sprintf("%dmo ago", diff/30)
	default:
		return fmt.Sprintf("%dy ago", diff/365)
	}
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

// trendingWindow is one of the time-window options on the /trending page.
type trendingWindow struct {
	Key    string
	Label  string
	Days   int // 0 = all time
	Active bool
}

func trendingWindows() []trendingWindow {
	return []trendingWindow{
		{Key: "week", Label: "Week", Days: 7},
		{Key: "month", Label: "Month", Days: 30},
		{Key: "90days", Label: "90 days", Days: 90},
		{Key: "alltime", Label: "All time", Days: 0},
	}
}

// trendingUserBar is one user's contribution to a recipe within the window.
type trendingUserBar struct {
	Username string
	Count    int
	Width    int  // percent 0-100, relative to the top contributor
	IsYou    bool
}

// trendingEntry is one row of the trending list.
type trendingEntry struct {
	Rank          int
	Recipe        generated.Recipe
	Score         float64
	LogsCounted   int
	DistinctUsers int
	Movement      string // "↑ N", "↓ N", "—", "new", or "" for all-time
	MovementCls   string // "up" / "down" / "flat" / "new" / ""
	UserBars      []trendingUserBar
	SparkLine     string // SVG polyline points (line)
	SparkArea     string // SVG polyline points (closed area under the line)
	SparkPeak     int    // peak weekly count, for tooltips/labels
}

const trendingHalfLife = 30.0

// computeTrendingScores walks `logs` and returns per-recipe trending score,
// per-user-per-recipe counts within the window, and total logs counted per
// recipe. Logs outside [windowStart, windowEnd] are ignored. windowEnd is
// inclusive (use today for current window, windowStart for previous).
func computeTrendingScores(logs []generated.ListRecentRecipeLogsRow, now time.Time, windowStart, windowEnd string) (scores map[int64]float64, logsCounted map[int64]int, perUser map[int64]map[string]int) {
	type pair struct {
		userID, recipeID int64
	}
	perUserPerRecipe := map[pair]int{}
	for _, l := range logs {
		if l.CookedOn < windowStart || l.CookedOn > windowEnd {
			continue
		}
		perUserPerRecipe[pair{l.UserID, l.RecipeID}]++
	}
	scores = map[int64]float64{}
	logsCounted = map[int64]int{}
	perUser = map[int64]map[string]int{}
	for _, l := range logs {
		if l.CookedOn < windowStart || l.CookedOn > windowEnd {
			continue
		}
		d, err := time.Parse("2006-01-02", l.CookedOn)
		if err != nil {
			continue
		}
		days := now.Sub(d).Hours() / 24
		if days < 0 {
			days = 0
		}
		decay := math.Pow(0.5, days/trendingHalfLife)
		n := perUserPerRecipe[pair{l.UserID, l.RecipeID}]
		dampener := (1 + math.Log2(float64(n))) / float64(n)
		scores[l.RecipeID] += decay * dampener
		logsCounted[l.RecipeID]++
		users, ok := perUser[l.RecipeID]
		if !ok {
			users = map[string]int{}
			perUser[l.RecipeID] = users
		}
		users[l.Username]++
	}
	return scores, logsCounted, perUser
}

// rankByScore returns recipe IDs sorted by score desc; the returned map gives
// 1-indexed rank by recipe ID.
func rankByScore(scores map[int64]float64) map[int64]int {
	type rs struct {
		id    int64
		score float64
	}
	ranked := make([]rs, 0, len(scores))
	for id, score := range scores {
		ranked = append(ranked, rs{id, score})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make(map[int64]int, len(ranked))
	for i, r := range ranked {
		out[r.id] = i + 1
	}
	return out
}

// sparkPolylines returns (line, area) point strings for a 13-week sparkline.
// `weekly` has length 13: index 0 is the oldest week, index 12 is current.
func sparkPolylines(weekly []int) (line, area string, peak int) {
	for _, v := range weekly {
		if v > peak {
			peak = v
		}
	}
	const w, h = 100.0, 36.0
	step := w / float64(len(weekly)-1)
	var lb, ab strings.Builder
	for i, v := range weekly {
		x := float64(i) * step
		var y float64 = h
		if peak > 0 {
			y = h - (float64(v)/float64(peak))*(h-4)
		}
		if i > 0 {
			lb.WriteByte(' ')
		}
		fmt.Fprintf(&lb, "%.1f,%.1f", x, y)
	}
	// Area: prepend bottom-left, append bottom-right.
	fmt.Fprintf(&ab, "0,%.1f ", h)
	ab.WriteString(lb.String())
	fmt.Fprintf(&ab, " %.1f,%.1f", w, h)
	return lb.String(), ab.String(), peak
}

func (s *server) handleTrending(w http.ResponseWriter, r *http.Request) {
	windows := trendingWindows()
	requested := r.URL.Query().Get("window")
	if requested == "" {
		requested = "month"
	}
	selected := -1
	for i := range windows {
		if windows[i].Key == requested {
			windows[i].Active = true
			selected = i
		}
	}
	if selected < 0 {
		selected = 1 // default: month
		windows[selected].Active = true
	}
	sel := windows[selected]

	// We always need at least 91 days of data for the sparkline plus, when
	// computing a finite window, the prior window of equal length to derive
	// rank movement. fetchDays covers the larger of the two needs.
	fetchDays := 91
	if sel.Days > 0 && sel.Days*2 > fetchDays {
		fetchDays = sel.Days * 2
	}
	if sel.Days == 0 {
		fetchDays = 365 * 5 // alltime: pull a generous span
	}
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -fetchDays).Format("2006-01-02")
	logs, err := s.queries.ListRecentRecipeLogs(r.Context(), cutoff)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	today := now.Format("2006-01-02")
	var windowStart string
	if sel.Days > 0 {
		windowStart = now.AddDate(0, 0, -sel.Days).Format("2006-01-02")
	} else {
		windowStart = "0000-01-01"
	}

	scores, logsCounted, perUser := computeTrendingScores(logs, now, windowStart, today)

	// Movement: rank in the prior window of equal length. Skipped for all-time.
	var prevRank map[int64]int
	if sel.Days > 0 {
		prevStart := now.AddDate(0, 0, -2*sel.Days).Format("2006-01-02")
		prevEnd := now.AddDate(0, 0, -sel.Days-1).Format("2006-01-02")
		prevScores, _, _ := computeTrendingScores(logs, now, prevStart, prevEnd)
		prevRank = rankByScore(prevScores)
	}

	// Sparkline: 13 weekly buckets over the past ~91 days regardless of window.
	sparkBuckets := map[int64][]int{}
	sparkStart := now.AddDate(0, 0, -91)
	for _, l := range logs {
		d, err := time.Parse("2006-01-02", l.CookedOn)
		if err != nil || d.Before(sparkStart) {
			continue
		}
		days := max(int(now.Sub(d).Hours()/24), 0)
		bucket := 12 - days/7
		if bucket < 0 || bucket > 12 {
			continue
		}
		buckets, ok := sparkBuckets[l.RecipeID]
		if !ok {
			buckets = make([]int, 13)
			sparkBuckets[l.RecipeID] = buckets
		}
		buckets[bucket]++
	}

	// Build entries in score-desc order.
	type rs struct {
		id    int64
		score float64
	}
	ranked := make([]rs, 0, len(scores))
	for id, score := range scores {
		ranked = append(ranked, rs{id, score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})

	ai := authInfoFromContext(r.Context())
	entries := make([]trendingEntry, 0, len(ranked))
	for i, item := range ranked {
		recipe, err := s.queries.GetRecipeByID(r.Context(), item.id)
		if err != nil {
			continue
		}
		rank := i + 1

		// Movement
		var movement, movementCls string
		if sel.Days > 0 {
			pr, was := prevRank[item.id]
			switch {
			case !was:
				movement, movementCls = "new", "new"
			case pr == rank:
				movement, movementCls = "—", "flat"
			case pr > rank:
				movement, movementCls = fmt.Sprintf("↑ %d", pr-rank), "up"
			default:
				movement, movementCls = fmt.Sprintf("↓ %d", rank-pr), "down"
			}
		}

		// User bars (sorted desc by count, ties by username asc)
		userCounts := perUser[item.id]
		bars := make([]trendingUserBar, 0, len(userCounts))
		var maxCount int
		for _, c := range userCounts {
			if c > maxCount {
				maxCount = c
			}
		}
		for u, c := range userCounts {
			width := 100
			if maxCount > 0 {
				width = c * 100 / maxCount
			}
			bars = append(bars, trendingUserBar{
				Username: u,
				Count:    c,
				Width:    width,
				IsYou:    ai.IsLoggedIn && u == ai.Username,
			})
		}
		sort.Slice(bars, func(i, j int) bool {
			if bars[i].Count != bars[j].Count {
				return bars[i].Count > bars[j].Count
			}
			return bars[i].Username < bars[j].Username
		})

		// Sparkline
		buckets := sparkBuckets[item.id]
		if buckets == nil {
			buckets = make([]int, 13)
		}
		line, area, peak := sparkPolylines(buckets)

		entries = append(entries, trendingEntry{
			Rank:          rank,
			Recipe:        recipe,
			Score:         item.score,
			LogsCounted:   logsCounted[item.id],
			DistinctUsers: len(userCounts),
			Movement:      movement,
			MovementCls:   movementCls,
			UserBars:      bars,
			SparkLine:     line,
			SparkArea:     area,
			SparkPeak:     peak,
		})
	}

	s.render(w, r, "trending.html", map[string]any{
		"Windows":  windows,
		"Selected": sel,
		"Entries":  entries,
	})
}
