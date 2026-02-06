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
	"syscall"
	"time"

	"github.com/danielmmetz/recipes/db/generated"
	"golang.org/x/sync/errgroup"

	_ "modernc.org/sqlite"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed db/schema.sql
var schemaSQL string

func main() {
	var addr string
	var port int
	flag.StringVar(&addr, "addr", "localhost", "address to listen on")
	flag.IntVar(&port, "port", 8080, "port to listen on")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := mainE(ctx, logger, fmt.Sprintf("%s:%d", addr, port)); err != nil {
		logger.ErrorContext(ctx, "exiting with error", slog.Any("err", err))
		// Only exit non-zero if our initial context has yet to be canceled.
		// Otherwise it's very likely that the error we're seeing is a result of our attempt at graceful shutdown.
		if ctx.Err() == nil {
			os.Exit(1)
		}
	}
}

func mainE(ctx context.Context, _ *slog.Logger, listenAddr string) error {
	db, err := sql.Open("sqlite", "recipes.db")
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}

	// Migration: add group_id column to ingredients if it doesn't exist.
	// SQLite doesn't support IF NOT EXISTS for ALTER TABLE, so we attempt the
	// alter and ignore the "duplicate column" error.
	_, _ = db.ExecContext(ctx, `ALTER TABLE ingredients ADD COLUMN group_id INTEGER REFERENCES ingredient_groups(id) ON DELETE CASCADE`)

	queries := generated.New(db)

	pages := map[string]*template.Template{}
	pageFiles := []string{"index.html", "view.html", "form.html"}
	for _, pf := range pageFiles {
		t, err := template.New("").Option("missingkey=error").ParseFS(templateFS,
			"templates/layout.html",
			"templates/ingredient_row.html",
			"templates/ingredient_group.html",
			"templates/"+pf,
		)
		if err != nil {
			return err
		}
		pages[pf] = t
	}
	partial, err := template.New("").Option("missingkey=error").ParseFS(templateFS, "templates/ingredient_row.html")
	if err != nil {
		return err
	}

	srv := &server{queries: queries, db: db, pages: pages, partial: partial}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.handleIndex)
	mux.HandleFunc("GET /recipes/new", srv.handleNewRecipe)
	mux.HandleFunc("POST /recipes", srv.handleCreateRecipe)
	mux.HandleFunc("GET /recipes/{slug}", srv.handleViewRecipe)
	mux.HandleFunc("GET /recipes/{slug}/edit", srv.handleEditRecipe)
	mux.HandleFunc("PUT /recipes/{slug}", srv.handleUpdateRecipe)
	mux.HandleFunc("DELETE /recipes/{slug}", srv.handleDeleteRecipe)
	mux.HandleFunc("GET /ingredients/row", srv.handleIngredientRow)

	s := http.Server{Addr: listenAddr, Handler: mux}
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


