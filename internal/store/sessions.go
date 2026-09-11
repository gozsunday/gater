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

type SessionStore struct {
	pool *pgxpool.Pool
}

type Session struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	TokenHash string    `json:"token_hash"`
	IPAddress *string   `json:"ip_address"`
	UserAgent *string   `json:"user_agent"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *SessionStore) Create(ctx context.Context, session *Session) error {
	query := `
    INSERT INTO sessions (user_id, token_hash, ip_address, user_agent, expires_at)
    VALUES ($1, $2, $3, $4, $5)
    RETURNING id, user_id, token_hash, ip_address, user_agent, expires_at, created_at,
    updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, session.UserID, session.TokenHash, session.IPAddress,
		session.UserAgent, session.ExpiresAt,
	).Scan(
		&session.ID, &session.UserID, &session.TokenHash, &session.IPAddress,
		&session.UserAgent, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt,
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

func (s *SessionStore) Get(ctx context.Context, hashedToken string) (*Session, error) {
	query := `
    SELECT id, user_id, token_hash, ip_address, user_agent, expires_at, created_at, updated_at
    FROM sessions
    WHERE token_hash = $1
    AND expires_at > $2
  `

	session := &Session{}
	err := s.pool.QueryRow(ctx, query, hashedToken, time.Now().UTC()).Scan(
		&session.ID, &session.UserID, &session.TokenHash, &session.IPAddress,
		&session.UserAgent, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return session, nil
}

func (s *SessionStore) Delete(ctx context.Context, sessionID uuid.UUID) error {
	query := `
    DELETE FROM sessions
    WHERE id = $1
  `

	if _, err := s.pool.Exec(ctx, query, sessionID); err != nil {
		return err
	}

	return nil
}

func (s *SessionStore) DeleteAll(ctx context.Context, userID uuid.UUID) error {
	query := `
    DELETE FROM sessions
    WHERE user_id = $1
  `

	if _, err := s.pool.Exec(ctx, query, userID); err != nil {
		return err
	}

	return nil
}
