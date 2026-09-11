package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OAuthStore struct {
	pool *pgxpool.Pool
}

type OAuthAccount struct {
	ID                uuid.UUID `json:"id"`
	UserID            uuid.UUID `json:"user_id"`
	Provider          string    `json:"provider"`
	ProviderAccountID string    `json:"provider_account_id"`
	Scope             string    `json:"scope"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (s *OAuthStore) GetByProviderAndAccountID(
	ctx context.Context,
	provider, accountID string,
) (*OAuthAccount, error) {
	query := `
    SELECT id, user_id, provider, provider_account_id, scope, created_at, updated_at
    FROM oauth_accounts
    WHERE provider = $1
    AND provider_account_id = $2
  `

	account := &OAuthAccount{}
	err := s.pool.QueryRow(ctx, query, provider, accountID).Scan(
		&account.ID, &account.UserID, &account.Provider, &account.ProviderAccountID,
		&account.Scope, &account.CreatedAt, &account.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return account, nil
}

func (s *OAuthStore) Create(ctx context.Context, account *OAuthAccount) error {
	query := `
    INSERT INTO oauth_accounts (
      user_id, provider, provider_account_id, scope
    )
    VALUES ($1, $2, $3, $4)
    RETURNING id, user_id, provider, provider_account_id, scope, created_at, updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, account.UserID, account.Provider, account.ProviderAccountID, account.Scope,
	).Scan(
		&account.ID, &account.UserID, &account.Provider, &account.ProviderAccountID,
		&account.Scope, &account.CreatedAt, &account.UpdatedAt,
	)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrConflict
		}
		return err
	}

	return nil
}
