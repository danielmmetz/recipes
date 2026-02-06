# Recipes

Personal recipe management web app. Go backend, SQLite (via sqlc), HTMX, Tailwind CSS (CDN).

## Run

```
go run . [-addr localhost] [-port 8080]
```

Creates `recipes.db` in the working directory on first run. Schema is applied automatically on startup.

## Development

- **SQL changes:** Edit `db/schema.sql` and `db/queries.sql`, then run `go run github.com/sqlc-dev/sqlc/cmd/sqlc generate`. Generated code lands in `db/generated/`.
- **Templates:** `templates/*.html` are embedded via `//go:embed`. Uses per-page template sets (layout + partials + page) for block inheritance. Templates use `missingkey=error`.
- **HTMX:** Delete uses `hx-delete` with `HX-Redirect` response header. Dynamic ingredient rows use `hx-get="/ingredients/row"` to append partials.
- **Routes:** Defined in `main.go`, handlers in `handlers.go`. Standard `net/http` mux with method-based routing (`GET /recipes/{slug}`, etc.).

## TODOs

### Basics

- [ ] create and use standard units for ingredients
    - this will also mean updating/migrating existing rows

### Bigger Functionality

- [ ] recipe scaling 
- [ ] tags
- [ ] import capabilities or similar
