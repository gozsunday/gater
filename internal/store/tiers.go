package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TiersStore struct {
	pool *pgxpool.Pool
}

type Tier struct {
	ID        uuid.UUID `json:"id"`
	EventID   uuid.UUID `json:"event_id"`
	Name      string    `json:"name"`
	Price     int       `json:"price"`
	Quantity  int       `json:"quantity"`
	Remaining int       `json:"remaining"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *TiersStore) Create(ctx context.Context, tier *Tier) error {
	query := `
    INSERT INTO ticket_tiers (event_id, name, price, quantity, remaining)
    VALUES ($1, $2, $3, $4, $5)
    RETURNING id, event_id, name, price, quantity, remaining, status,
    created_at, updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, tier.EventID, tier.Name, tier.Price, tier.Quantity, tier.Remaining,
	).Scan(
		&tier.ID, &tier.EventID, &tier.Name, &tier.Price, &tier.Quantity,
		&tier.Remaining, &tier.Status, &tier.CreatedAt, &tier.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: create tier: %w", err)
	}

	return nil
}

func (s *TiersStore) Delete(ctx context.Context, id, eventID string) error {
	query := `
    DELETE FROM ticket_tiers
    WHERE id = $1 AND event_id = $2
  `

	ct, err := s.pool.Exec(ctx, query, id, eventID)
	if err != nil {
		return fmt.Errorf("store: delete tier: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store: delete tier: %w", ErrNotFound)
	}

	return nil
}

func (s *TiersStore) CountByEvent(ctx context.Context, eventID string) (int, error) {
	query := `
    SELECT COUNT(*)
    FROM ticket_tiers
    WHERE event_id = $1
  `

	var count int
	err := s.pool.QueryRow(ctx, query, eventID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count tiers by event: %w", err)
	}

	return count, nil
}

func (s *TiersStore) GetByID(ctx context.Context, id string) (*Tier, error) {
	query := `
    SELECT id, event_id, name, price, quantity, remaining, status,
    created_at, updated_at
    FROM ticket_tiers
    WHERE id = $1
  `

	tier := &Tier{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&tier.ID, &tier.EventID, &tier.Name, &tier.Price, &tier.Quantity,
		&tier.Remaining, &tier.Status, &tier.CreatedAt, &tier.UpdatedAt,
	)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("store: get tier by id: %w", ErrNotFound)
		default:
			return nil, fmt.Errorf("store: get tier by id: %w", err)
		}
	}

	return tier, nil
}

func (s *TiersStore) Update(ctx context.Context, tier *Tier) error {
	query := `
    UPDATE ticket_tiers
    SET name = $2, price = $3, quantity = $4, remaining = $5, status = $6
    WHERE id = $1
    RETURNING id, event_id, name, price, quantity, remaining, status,
    created_at, updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, tier.ID, tier.Name, tier.Price, tier.Quantity,
		tier.Remaining, tier.Status,
	).Scan(
		&tier.ID, &tier.EventID, &tier.Name, &tier.Price, &tier.Quantity,
		&tier.Remaining, &tier.Status, &tier.CreatedAt, &tier.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: update tier: %w", err)
	}

	return nil
}

func (s *TiersStore) ListByEvent(ctx context.Context, eventID string) ([]*Tier, error) {
	query := `
    SELECT id, event_id, name, price, quantity, remaining, status,
    created_at, updated_at
    FROM ticket_tiers
    WHERE event_id = $1
    ORDER BY created_at ASC
  `

	rows, err := s.pool.Query(ctx, query, eventID)
	if err != nil {
		return nil, fmt.Errorf("store: list tiers by event: %w", err)
	}
	defer rows.Close()

	tiers, err := pgx.CollectRows(rows, pgx.RowToAddrOf[Tier])
	if err != nil {
		return nil, fmt.Errorf("store: collect tiers by event: %w", err)
	}

	return tiers, nil
}

func (s *TiersStore) SumQuantityByEvent(ctx context.Context, eventID string) (int, error) {
	query := `
    SELECT COALESCE(SUM(quantity), 0)
    FROM ticket_tiers
    WHERE event_id = $1
  `

	var sum int
	err := s.pool.QueryRow(ctx, query, eventID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("store: sum tier quantities by event: %w", err)
	}

	return sum, nil
}
