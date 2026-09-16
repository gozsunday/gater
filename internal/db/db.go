package db

import (
	"context"
	"fmt"

	"github.com/gozsunday/gater/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	// get DB config struct from DB URL
	dbCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}

	// create pool from config
	pool, err := pgxpool.NewWithConfig(ctx, dbCfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	// ping database
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping database: %w", err)
	}

	return pool, nil
}
