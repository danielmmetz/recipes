# Recipes

Personal recipe management app. Go backend, SQLite (via sqlc), HTMX, Tailwind CSS (CDN).

## Usage

```
go run . <subcommand> -h
```

| Subcommand | Description |
|---|---|
| `server` | Start the web server |
| `list-recipes` | List all recipes (id, slug, title) |
| `get-recipe` | Display a recipe as markdown |
| `put-recipe` | Create or update a recipe |

Creates `recipes.db` in the working directory on first run. Schema is applied automatically on startup.

## Development

- **SQL changes:** Edit `db/schema.sql` and `db/queries.sql`, then run `go run github.com/sqlc-dev/sqlc/cmd/sqlc generate`. Generated code lands in `db/generated/`.
- **Templates:** `templates/*.html` are embedded via `//go:embed`. Uses per-page template sets (layout + partials + page) for block inheritance. Templates use `missingkey=error`.
- **HTMX:** Delete uses `hx-delete` with `HX-Redirect` response header. Dynamic ingredient rows use `hx-get="/ingredients/row"` to append partials.
- **Routes:** Defined in `main.go`, handlers in `handlers.go`. Standard `net/http` mux with method-based routing (`GET /recipes/{slug}`, etc.).
