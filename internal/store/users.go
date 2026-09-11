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

type UserStore struct {
	pool *pgxpool.Pool
}

type User struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	PasswordHash  *string   `json:"-"`
	EmailVerified bool      `json:"email_verified"`
	Image         *string   `json:"image"`
	Role          string    `json:"role"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *UserStore) Create(ctx context.Context, user *User) error {
	query := `
    INSERT INTO users (name, email, password_hash, image, email_verified)
    VALUES ($1, $2, $3, $4, $5)
    RETURNING id, name, email, password_hash, email_verified, image, role, created_at, updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, user.Name, user.Email, user.PasswordHash,
		user.Image, user.EmailVerified,
	).Scan(
		&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.EmailVerified,
		&user.Image, &user.Role, &user.CreatedAt, &user.UpdatedAt,
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

func (s *UserStore) GetByID(ctx context.Context, id string) (*User, error) {
	query := `
    SELECT id, name, email, password_hash, email_verified, image, role, created_at, updated_at
    FROM users
    WHERE id = $1
  `

	user := &User{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.EmailVerified,
		&user.Image, &user.Role, &user.CreatedAt, &user.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return user, nil
}

func (s *UserStore) GetByEmail(ctx context.Context, email string) (*User, error) {
	query := `
    SELECT id, name, email, password_hash, email_verified, image, role, created_at, updated_at
    FROM users 
    WHERE email = $1
  `

	user := &User{}
	err := s.pool.QueryRow(ctx, query, email).Scan(
		&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.EmailVerified,
		&user.Image, &user.Role, &user.CreatedAt, &user.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return user, nil
}

func (s *UserStore) MarkVerified(ctx context.Context, email string) error {
	query := `
    UPDATE users 
    SET email_verified = $2
    WHERE email = $1
  `

	ct, err := s.pool.Exec(ctx, query, email, true)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *UserStore) ResetPassword(ctx context.Context, email, hashedPassword string) error {
	query := `
    UPDATE users 
    SET password_hash = $2
    WHERE email = $1
  `

	ct, err := s.pool.Exec(ctx, query, email, hashedPassword)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *UserStore) BecomeOrganizer(ctx context.Context, userID string) (*User, error) {
	query := `
    UPDATE users
    SET role = $2
    WHERE id = $1
    RETURNING id, name, email, password_hash, email_verified, image, role, created_at, updated_at
  `

	user := &User{}
	err := s.pool.QueryRow(ctx, query, userID, RoleOrganizer).Scan(
		&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.EmailVerified,
		&user.Image, &user.Role, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return user, nil
}
