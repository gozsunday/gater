package testdb

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gozsunday/gater/cmd/migrate/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

// `Open` returns a migrated connection pool for the isolated gater_test db
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()
	dbURL := getDatabaseURL()

	ensureDatabase(t, dbURL)
	migrate(t, dbURL)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("testdb: open pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("testdb: ping test database: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// `Truncate` removes test data while preserving schema and goose history
func Truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(context.Background(), `
		TRUNCATE TABLE
			users,
			sessions,
			verifications,
			oauth_accounts,
			events,
			ticket_tiers,
			purchases,
			tickets,
			waitlist_entries
		RESTART IDENTITY CASCADE
	`)
	if err != nil {
		t.Fatalf("testdb: truncate tables: %v", err)
	}
}

func getDatabaseURL() string {
	if dbURL := os.Getenv("TEST_DATABASE_URL"); dbURL != "" {
		return dbURL
	}

	return "postgresql://user:secret@localhost:5435/gater_test?sslmode=disable"
}

func ensureDatabase(t *testing.T, dbURL string) {
	t.Helper()

	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatalf("testdb: parse database URL: %v", err)
	}

	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		t.Fatalf("testdb: database name missing in TEST_DATABASE_URL")
	}
	// overwrite u.Path with "/postgres" so as to run the
	// `CREATE DATABASE` query
	u.Path = "/postgres"

	admin, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatalf("testdb: open maintenance database: %v", err)
	}
	defer admin.Close()

	if err := admin.Ping(); err != nil {
		t.Fatalf("testdb: ping maintenance database: %v", err)
	}

	_, err = admin.Exec("CREATE DATABASE " + quoteIdentifier(name))
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "42P04" {
			return
		}
		t.Fatalf("testdb: create test database: %v", err)
	}
}

func migrate(t *testing.T, dbURL string) {
	t.Helper()

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("testdb: open migration database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("testdb: ping migration database: %v", err)
	}

	// set up goose with embedded migrations
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("testdb: set migration dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("testdb: run migrations: %v", err)
	}
}

// quoteIdentifier safely quotes a database name supplied through the
// environment before interpolating it into CREATE DATABASE
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
