package main

import (
	"context"
	"database/sql"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/danielmmetz/recipes/db/generated"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"golang.org/x/sync/errgroup"

	_ "modernc.org/sqlite"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed db/schema.sql
var schemaSQL string

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := mainE(ctx, os.Args[1:]); err != nil && err != flag.ErrHelp {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func mainE(ctx context.Context, args []string) error {
	root := &ffcli.Command{
		ShortUsage:  "recipes <subcommand> [flags]",
		ShortHelp:   "Recipe management tool.",
		Subcommands: []*ffcli.Command{serverCommand(), listRecipesCommand(), getRecipeCommand(), putRecipeCommand()},
		Exec: func(ctx context.Context, args []string) error {
			return flag.ErrHelp
		},
	}

	return root.ParseAndRun(ctx, args)
}

func serverCommand() *ffcli.Command {
	var (
		fs             = flag.NewFlagSet("server", flag.ExitOnError)
		db             string
		addr           string
		port           int
		oidcClientID   string
		oidcClientSecret string
		oidcIssuerURL  string
		baseURL        string
	)
	fs.StringVar(&db, "db", "recipes.db", "path to SQLite database")
	fs.StringVar(&addr, "addr", "localhost", "address to listen on")
	fs.IntVar(&port, "port", 8080, "port to listen on")
	fs.StringVar(&oidcClientID, "oidc-client-id", "", "OIDC client ID")
	fs.StringVar(&oidcClientSecret, "oidc-client-secret", "", "OIDC client secret")
	fs.StringVar(&oidcIssuerURL, "oidc-issuer-url", "https://idp.example.com", "OIDC issuer URL")
	fs.StringVar(&baseURL, "base-url", "http://localhost:8080", "Base URL for this app (used for OIDC redirect URI and cookie settings)")

	return &ffcli.Command{
		Name:       "server",
		ShortUsage: "recipes server [flags]",
		ShortHelp:  "Start the web server.",
		FlagSet:    fs,
		Options:    []ff.Option{ff.WithEnvVars()},
		Exec: func(ctx context.Context, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			authCfg := authConfig{
				ClientID:     oidcClientID,
				ClientSecret: oidcClientSecret,
				IssuerURL:    oidcIssuerURL,
				BaseURL:      baseURL,
				SecureCookie: strings.HasPrefix(baseURL, "https://"),
			}
			if !authCfg.enabled() {
				logger.Info("OIDC not configured, auth disabled (set OIDC_CLIENT_ID and OIDC_CLIENT_SECRET to enable)")
			}
			return runServer(ctx, logger, db, fmt.Sprintf("%s:%d", addr, port), authCfg)
		},
	}
}

func listRecipesCommand() *ffcli.Command {
	var (
		fs = flag.NewFlagSet("list-recipes", flag.ExitOnError)
		db string
	)
	fs.StringVar(&db, "db", "recipes.db", "path to SQLite database")

	return &ffcli.Command{
		Name:       "list-recipes",
		ShortUsage: "recipes list-recipes",
		ShortHelp:  "List all recipes (id, slug, title).",
		FlagSet:    fs,
		Exec: func(ctx context.Context, args []string) error {
			return runListRecipes(ctx, db)
		},
	}
}

func getRecipeCommand() *ffcli.Command {
	var (
		fs   = flag.NewFlagSet("get-recipe", flag.ExitOnError)
		db   string
		slug string
		id   int64
	)
	fs.StringVar(&db, "db", "recipes.db", "path to SQLite database")
	fs.StringVar(&slug, "slug", "", "recipe slug")
	fs.Int64Var(&id, "id", 0, "recipe ID")

	return &ffcli.Command{
		Name:       "get-recipe",
		ShortUsage: "recipes get-recipe -slug <slug> | -id <id>",
		ShortHelp:  "Get a recipe and display it as markdown.",
		FlagSet:    fs,
		Exec: func(ctx context.Context, args []string) error {
			if (slug == "" && id == 0) || (slug != "" && id != 0) {
				return fmt.Errorf("exactly one of -slug or -id is required")
			}
			return runGetRecipe(ctx, db, slug, id)
		},
	}
}

func putRecipeCommand() *ffcli.Command {
	var (
		fs           = flag.NewFlagSet("put-recipe", flag.ExitOnError)
		db           string
		title        string
		instructions string
		source       string
		private      bool
		ingredients  repeatedFlag
		tags         repeatedFlag
	)
	fs.StringVar(&db, "db", "recipes.db", "path to SQLite database")
	fs.StringVar(&title, "title", "", "recipe title (required)")
	fs.StringVar(&instructions, "instructions", "", "recipe instructions (markdown)")
	fs.StringVar(&source, "source", "", "recipe source URL or description")
	fs.BoolVar(&private, "private", false, "mark recipe as private (visible only to admins)")
	fs.Var(&ingredients, "ingredient", `ingredient in "qty unit name" or "name" format, e.g. "2 cup flour" (repeatable)`)
	fs.Var(&tags, "tag", "tag name (repeatable)")

	return &ffcli.Command{
		Name:       "put-recipe",
		ShortUsage: "recipes put-recipe -title <title> [flags]",
		ShortHelp:  "Create or update a recipe (upsert by slug derived from title).",
		FlagSet:    fs,
		Exec: func(ctx context.Context, args []string) error {
			if title == "" {
				return fmt.Errorf("-title is required")
			}
			return runPutRecipe(ctx, db, title, source, instructions, private, ingredients, tags)
		},
	}
}

// repeatedFlag implements flag.Value for flags that can be specified multiple times.
type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ", ") }
func (f *repeatedFlag) Set(val string) error {
	*f = append(*f, val)
	return nil
}

func openDB(ctx context.Context, dbPath string) (*sql.DB, *generated.Queries, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("enabling foreign keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("applying schema: %w", err)
	}
	// Migrations
	_, _ = db.ExecContext(ctx, `ALTER TABLE ingredients ADD COLUMN group_id INTEGER REFERENCES ingredient_groups(id) ON DELETE CASCADE`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE recipes ADD COLUMN source TEXT NOT NULL DEFAULT ''`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE recipes ADD COLUMN private INTEGER NOT NULL DEFAULT 0`)

	return db, generated.New(db), nil
}

func runServer(ctx context.Context, logger *slog.Logger, dbPath string, listenAddr string, authCfg authConfig) error {
	db, queries, err := openDB(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	funcMap := template.FuncMap{
		"fmtQty": formatQuantity,
	}

	pages := map[string]*template.Template{}
	pageFiles := []string{"index.html", "view.html", "form.html"}
	for _, pf := range pageFiles {
		t, err := template.New("").Funcs(funcMap).Option("missingkey=error").ParseFS(templateFS,
			"templates/layout.html",
			"templates/ingredient_row.html",
			"templates/ingredient_group.html",
			"templates/"+pf,
		)
		if err != nil {
			return fmt.Errorf("parsing template %q: %w", pf, err)
		}
		pages[pf] = t
	}
	partial, err := template.New("").Funcs(funcMap).Option("missingkey=error").ParseFS(templateFS, "templates/ingredient_row.html")
	if err != nil {
		return fmt.Errorf("parsing ingredient_row partial: %w", err)
	}

	srv := &server{
		queries:     queries,
		db:          db,
		pages:       pages,
		partial:     partial,
		auth:        authCfg,
		sessions:    newSessionStore(),
		authPending: newAuthState(),
		logger:      logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.handleIndex)
	mux.HandleFunc("GET /recipes/new", srv.requiresAdmin(srv.handleNewRecipe))
	mux.HandleFunc("POST /recipes", srv.requiresAdmin(srv.handleCreateRecipe))
	mux.HandleFunc("GET /recipes/{slug}", srv.handleViewRecipe)
	mux.HandleFunc("GET /recipes/{slug}/edit", srv.requiresAdmin(srv.handleEditRecipe))
	mux.HandleFunc("PUT /recipes/{slug}", srv.requiresAdmin(srv.handleUpdateRecipe))
	mux.HandleFunc("DELETE /recipes/{slug}", srv.requiresAdmin(srv.handleDeleteRecipe))
	mux.HandleFunc("GET /ingredients/row", srv.handleIngredientRow)
	mux.HandleFunc("GET /auth/login", srv.handleLogin)
	mux.HandleFunc("GET /auth/callback", srv.handleCallback)
	mux.HandleFunc("POST /auth/logout", srv.handleLogout)

	s := http.Server{Addr: listenAddr, Handler: srv.authMiddleware(mux)}
	var eg errgroup.Group
	eg.Go(func() error {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return s.Shutdown(ctx)
	})

	switch err := s.ListenAndServe(); err {
	case http.ErrServerClosed:
		return eg.Wait()
	default:
		return err
	}
}

func runListRecipes(ctx context.Context, dbPath string) error {
	db, queries, err := openDB(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	recipes, err := queries.ListRecipes(ctx)
	if err != nil {
		return fmt.Errorf("listing recipes: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSLUG\tTITLE")
	for _, r := range recipes {
		fmt.Fprintf(w, "%d\t%s\t%s\n", r.ID, r.Slug, r.Title)
	}
	return w.Flush()
}

func runGetRecipe(ctx context.Context, dbPath string, slug string, id int64) error {
	db, queries, err := openDB(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	var recipe generated.Recipe
	if slug != "" {
		recipe, err = queries.GetRecipeBySlug(ctx, slug)
	} else {
		recipe, err = queries.GetRecipeByID(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("getting recipe: %w", err)
	}

	ingredients, err := queries.ListIngredientsByRecipeID(ctx, recipe.ID)
	if err != nil {
		return fmt.Errorf("listing ingredients: %w", err)
	}
	groups, err := queries.ListIngredientGroupsByRecipeID(ctx, recipe.ID)
	if err != nil {
		return fmt.Errorf("listing ingredient groups: %w", err)
	}
	tags, err := queries.ListTagsByRecipeID(ctx, recipe.ID)
	if err != nil {
		return fmt.Errorf("listing tags: %w", err)
	}

	// Build markdown output
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", recipe.Title)

	if recipe.Source != "" {
		fmt.Fprintf(&b, "**Source:** %s\n\n", recipe.Source)
	}

	if len(tags) > 0 {
		names := make([]string, len(tags))
		for i, t := range tags {
			names[i] = t.Name
		}
		fmt.Fprintf(&b, "**Tags:** %s\n\n", strings.Join(names, ", "))
	}

	if len(ingredients) > 0 {
		b.WriteString("## Ingredients\n\n")

		// Ungrouped first
		for _, ing := range ingredients {
			if !ing.GroupID.Valid {
				fmt.Fprintf(&b, "- %s\n", formatIngredient(ing))
			}
		}

		// Then grouped
		for _, g := range groups {
			fmt.Fprintf(&b, "\n### %s\n\n", g.Name)
			for _, ing := range ingredients {
				if ing.GroupID.Valid && ing.GroupID.Int64 == g.ID {
					fmt.Fprintf(&b, "- %s\n", formatIngredient(ing))
				}
			}
		}
		b.WriteString("\n")
	}

	if recipe.Instructions != "" {
		b.WriteString("## Instructions\n\n")
		b.WriteString(recipe.Instructions)
		b.WriteString("\n")
	}

	fmt.Print(b.String())
	return nil
}

// formatQuantity renders a float64 as a human-friendly string.
// Whole numbers are shown without decimals; common fractions (thirds, quarters,
// sixths, eighths) are displayed as vulgar-fraction Unicode characters; all
// other values use %g (compact, no trailing zeros).
func formatQuantity(v float64) string {
	// Map of fractional parts to their Unicode vulgar fraction representations.
	type frac struct {
		value float64
		str   string
	}
	fractions := []frac{
		{1.0 / 8, "⅛"},
		{1.0 / 6, "⅙"},
		{1.0 / 4, "¼"},
		{1.0 / 3, "⅓"},
		{3.0 / 8, "⅜"},
		{1.0 / 2, "½"},
		{5.0 / 8, "⅝"},
		{2.0 / 3, "⅔"},
		{3.0 / 4, "¾"},
		{5.0 / 6, "⅚"},
		{7.0 / 8, "⅞"},
	}
	const tol = 0.001

	whole := int64(v)
	fpart := v - float64(whole)

	// Pure whole number
	if fpart < tol {
		return fmt.Sprintf("%d", whole)
	}
	// Check if the fractional part is close to 1 (rounds up)
	if 1-fpart < tol {
		return fmt.Sprintf("%d", whole+1)
	}

	for _, f := range fractions {
		if abs(fpart-f.value) < tol {
			if whole == 0 {
				return f.str
			}
			return fmt.Sprintf("%d%s", whole, f.str)
		}
	}

	// Fallback: use %g for a compact representation
	return fmt.Sprintf("%g", v)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func formatIngredient(ing generated.Ingredient) string {
	var parts []string
	if ing.Quantity.Valid {
		q := ing.Quantity.Float64
		if q == float64(int64(q)) {
			parts = append(parts, fmt.Sprintf("%d", int64(q)))
		} else {
			parts = append(parts, fmt.Sprintf("%g", q))
		}
	}
	if ing.Unit.Valid && ing.Unit.String != "" {
		parts = append(parts, ing.Unit.String)
	}
	parts = append(parts, ing.Name)
	return strings.Join(parts, " ")
}

func runPutRecipe(ctx context.Context, dbPath string, title, source, instructions string, private bool, ingredients, tags []string) error {
	db, queries, err := openDB(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	slug := slugify(title)

	// Check if recipe exists
	existing, err := queries.GetRecipeBySlug(ctx, slug)
	if err != nil {
		// Create
		var privateInt int64
		if private {
			privateInt = 1
		}
		recipe, err := queries.CreateRecipe(ctx, generated.CreateRecipeParams{
			Slug:         slug,
			Title:        title,
			Source:       source,
			Instructions: instructions,
			Private:      privateInt,
		})
		if err != nil {
			return fmt.Errorf("creating recipe: %w", err)
		}
		if err := saveCLIIngredients(ctx, queries, recipe.ID, ingredients); err != nil {
			return fmt.Errorf("saving ingredients for new recipe: %w", err)
		}
		if err := saveCLITags(ctx, queries, recipe.ID, tags); err != nil {
			return fmt.Errorf("saving tags for new recipe: %w", err)
		}
		fmt.Printf("Created recipe: %s (id=%d)\n", slug, recipe.ID)
		return nil
	}

	// Update
	var privateInt int64
	if private {
		privateInt = 1
	}
	if err := queries.UpdateRecipe(ctx, generated.UpdateRecipeParams{
		Title:        title,
		Slug:         slug,
		Source:       source,
		Instructions: instructions,
		Private:      privateInt,
		Slug_2:       slug,
	}); err != nil {
		return fmt.Errorf("updating recipe: %w", err)
	}

	// Clear and re-create ingredients
	if err := queries.DeleteIngredientsByRecipeID(ctx, existing.ID); err != nil {
		return fmt.Errorf("deleting existing ingredients: %w", err)
	}
	if err := queries.DeleteIngredientGroupsByRecipeID(ctx, existing.ID); err != nil {
		return fmt.Errorf("deleting existing ingredient groups: %w", err)
	}
	if err := saveCLIIngredients(ctx, queries, existing.ID, ingredients); err != nil {
		return fmt.Errorf("saving ingredients for updated recipe: %w", err)
	}
	if err := saveCLITags(ctx, queries, existing.ID, tags); err != nil {
		return fmt.Errorf("saving tags for updated recipe: %w", err)
	}
	fmt.Printf("Updated recipe: %s (id=%d)\n", slug, existing.ID)
	return nil
}

// saveCLIIngredients parses ingredient strings and creates them.
// Format: "qty unit name" or just "name". All ingredients are ungrouped.
func saveCLIIngredients(ctx context.Context, queries *generated.Queries, recipeID int64, ingredients []string) error {
	for i, raw := range ingredients {
		qty, unit, name := parseIngredientString(raw)
		if name == "" {
			continue
		}
		if err := queries.CreateIngredient(ctx, generated.CreateIngredientParams{
			RecipeID:  recipeID,
			Quantity:  qty,
			Unit:      unit,
			Name:      name,
			SortOrder: int64(i),
		}); err != nil {
			return fmt.Errorf("creating ingredient %q: %w", raw, err)
		}
	}
	return nil
}

func saveCLITags(ctx context.Context, queries *generated.Queries, recipeID int64, tags []string) error {
	if err := queries.DeleteRecipeTagsByRecipeID(ctx, recipeID); err != nil {
		return fmt.Errorf("deleting existing recipe tags: %w", err)
	}
	for _, name := range tags {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		tag, err := queries.GetOrCreateTag(ctx, name)
		if err != nil {
			return fmt.Errorf("creating tag %q: %w", name, err)
		}
		if err := queries.AddRecipeTag(ctx, generated.AddRecipeTagParams{
			RecipeID: recipeID,
			TagID:    tag.ID,
		}); err != nil {
			return fmt.Errorf("adding tag %q to recipe: %w", name, err)
		}
	}
	if err := queries.DeleteOrphanedTags(ctx); err != nil {
		return fmt.Errorf("deleting orphaned tags: %w", err)
	}
	return nil
}

// parseIngredientString parses "qty unit name", "qty name", or "name".
func parseIngredientString(s string) (sql.NullFloat64, sql.NullString, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullFloat64{}, sql.NullString{}, ""
	}

	parts := strings.Fields(s)
	if len(parts) == 1 {
		return sql.NullFloat64{}, sql.NullString{}, parts[0]
	}

	// Try to parse first token as a number
	var qty sql.NullFloat64
	var rest []string
	if f, err := parseFloat(parts[0]); err == nil {
		qty = sql.NullFloat64{Float64: f, Valid: true}
		rest = parts[1:]
	} else {
		// No quantity — entire string is the name
		return sql.NullFloat64{}, sql.NullString{}, s
	}

	if len(rest) == 0 {
		return qty, sql.NullString{}, ""
	}

	// Check if next token is a known unit
	var unit sql.NullString
	if IsValidUnit(rest[0]) {
		unit = sql.NullString{String: rest[0], Valid: true}
		rest = rest[1:]
	}

	name := strings.Join(rest, " ")
	return qty, unit, name
}

func parseFloat(s string) (float64, error) {
	// Try plain decimal first
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}

	// Try fraction like "1/2"
	if num, denom, ok := strings.Cut(s, "/"); ok {
		n, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid numerator %q: %w", num, err)
		}
		d, err := strconv.ParseFloat(denom, 64)
		if err != nil || d == 0 {
			return 0, fmt.Errorf("invalid denominator %q", denom)
		}
		return n / d, nil
	}

	return 0, fmt.Errorf("cannot parse %q as a number", s)
}
