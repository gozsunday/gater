package main

import (
	"database/sql"
	"embed"
	"log/slog"
	"os"

	"github.com/gozsunday/gater/internal/config"
	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	defer db.Close()

	// set up goose with embedded migrations
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		slog.Error("failed to set dialect", "error", err)
		os.Exit(1)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("migrations successful!")
}
