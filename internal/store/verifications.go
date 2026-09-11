package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type VerificationStore struct {
	pool *pgxpool.Pool
}

type Verifications struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	Value      string    `json:"value"`
	ExpiresAt  time.Time `json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type CreateVerificationParams struct {
	Identifier  string
	HashedToken string
	ExpiresAt   time.Time
}

func (v *VerificationStore) Create(ctx context.Context, params CreateVerificationParams) error {
	query := `
    INSERT INTO verifications (identifier, value, expires_at)
    VALUES ($1, $2, $3)
  `

	_, err := v.pool.Exec(
		ctx, query, params.Identifier,
		params.HashedToken, params.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("store: create verification: %w", err)
	}

	return nil
}

func (v *VerificationStore) Get(ctx context.Context, hashedToken string) (*Verifications, error) {
	query := `
    SELECT id, identifier, value, expires_at, created_at, updated_at
    FROM verifications
    WHERE value = $1
  `

	verification := &Verifications{}
	err := v.pool.QueryRow(ctx, query, hashedToken).Scan(
		&verification.ID, &verification.Identifier, &verification.Value, &verification.ExpiresAt,
		&verification.CreatedAt, &verification.UpdatedAt,
	)

	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("store: get verification: %w", ErrNotFound)
		default:
			return nil, fmt.Errorf("store: get verification: %w", err)
		}
	}

	return verification, nil
}

func (v *VerificationStore) GetLatest(ctx context.Context, identifier string) (*Verifications, error) {
	query := `
    SELECT id, identifier, value, expires_at, created_at, updated_at
    FROM verifications
    WHERE identifier = $1
    ORDER BY created_at DESC
    LIMIT 1
  `

	verification := &Verifications{}
	err := v.pool.QueryRow(ctx, query, identifier).Scan(
		&verification.ID, &verification.Identifier, &verification.Value, &verification.ExpiresAt,
		&verification.CreatedAt, &verification.UpdatedAt,
	)

	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("store: get latest verification: %w", ErrNotFound)
		default:
			return nil, fmt.Errorf("store: get latest verification: %w", err)
		}
	}

	return verification, nil
}

func (v *VerificationStore) CountSince(ctx context.Context, identifier string, since time.Duration) (int, error) {
	query := `
    SELECT COUNT(*)
    FROM verifications
    WHERE identifier = $1
    AND created_at > $2
  `

	count := 0
	timeCutoff := time.Now().UTC().Add(-since)

	err := v.pool.QueryRow(ctx, query, identifier, timeCutoff).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count verifications since: %w", err)
	}

	return count, nil
}

func (v *VerificationStore) Delete(ctx context.Context, ID string) error {
	query := `
    DELETE FROM verifications
    WHERE id = $1
  `

	if _, err := v.pool.Exec(ctx, query, ID); err != nil {
		return fmt.Errorf("store: delete verification: %w", err)
	}

	return nil
}

func (v *VerificationStore) DeleteByIdentifier(ctx context.Context, identifier string) error {
	query := `
    DELETE FROM verifications
    WHERE identifier = $1
  `

	if _, err := v.pool.Exec(ctx, query, identifier); err != nil {
		return fmt.Errorf("store: delete verification by identifier: %w", err)
	}

	return nil
}
