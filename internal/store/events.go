package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/goziemsunday/gater/internal/cursor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EventsStore struct {
	pool *pgxpool.Pool
}

type Event struct {
	ID                      uuid.UUID  `json:"id"`
	OrganizerID             uuid.UUID  `json:"organizer_id"`
	Name                    string     `json:"name"`
	Description             *string    `json:"description"`
	Location                string     `json:"location"`
	Status                  string     `json:"status"`
	StartsAt                time.Time  `json:"starts_at"`
	EndsAt                  time.Time  `json:"ends_at"`
	Capacity                *int       `json:"capacity"`
	CancellationAllowed     bool       `json:"cancellation_allowed"`
	CancellationHoursBefore int        `json:"cancellation_hours_before"`
	MaxTicketsPerPurchase   int        `json:"max_tickets_per_purchase"`
	MaterialChangedAt       *time.Time `json:"material_changed_at"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

func (s *EventsStore) Create(ctx context.Context, event *Event) error {
	query := `
    INSERT INTO events (
      organizer_id, name, description, location, starts_at, ends_at,
      capacity, cancellation_allowed, cancellation_hours_before,
      max_tickets_per_purchase
    )
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    RETURNING id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, event.OrganizerID, event.Name, event.Description, event.Location,
		event.StartsAt, event.EndsAt, event.Capacity, event.CancellationAllowed,
		event.CancellationHoursBefore, event.MaxTicketsPerPurchase,
	).Scan(
		&event.ID, &event.OrganizerID, &event.Name, &event.Description,
		&event.Location, &event.Status, &event.StartsAt, &event.EndsAt,
		&event.Capacity, &event.CancellationAllowed, &event.CancellationHoursBefore,
		&event.MaxTicketsPerPurchase, &event.MaterialChangedAt, &event.CreatedAt, &event.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: create event: %w", err)
	}

	return nil
}

func (s *EventsStore) Update(ctx context.Context, event *Event) error {
	// COALESCE($11, material_changed_at): the handler only passes a stamp
	// when a confirmed material change is landing; nil on every other update
	// keeps any existing stamp intact instead of wiping grace windows away
	query := `
    UPDATE events
    SET name = $2, description = $3, location = $4, starts_at = $5,
    ends_at = $6, capacity = $7, cancellation_allowed = $8,
    cancellation_hours_before = $9, max_tickets_per_purchase = $10,
    material_changed_at = COALESCE($11, material_changed_at)
    WHERE id = $1
    RETURNING id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
  `

	err := s.pool.QueryRow(
		ctx, query, event.ID, event.Name, event.Description, event.Location,
		event.StartsAt, event.EndsAt, event.Capacity, event.CancellationAllowed,
		event.CancellationHoursBefore, event.MaxTicketsPerPurchase,
		event.MaterialChangedAt,
	).Scan(
		&event.ID, &event.OrganizerID, &event.Name, &event.Description,
		&event.Location, &event.Status, &event.StartsAt, &event.EndsAt,
		&event.Capacity, &event.CancellationAllowed, &event.CancellationHoursBefore,
		&event.MaxTicketsPerPurchase, &event.MaterialChangedAt, &event.CreatedAt, &event.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: update event: %w", err)
	}

	return nil
}

func (s *EventsStore) Delete(ctx context.Context, id string) error {
	query := `
    DELETE FROM events
    WHERE id = $1
  `

	ct, err := s.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("store: delete event: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store: delete event: %w", ErrNotFound)
	}

	return nil
}

func (s *EventsStore) Publish(ctx context.Context, id string) (*Event, error) {
	query := `
    UPDATE events
    SET status = 'published'
    WHERE id = $1 AND status = 'draft'
    RETURNING id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
  `

	event := &Event{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&event.ID, &event.OrganizerID, &event.Name, &event.Description, &event.Location,
		&event.Status, &event.StartsAt, &event.EndsAt, &event.Capacity,
		&event.CancellationAllowed, &event.CancellationHoursBefore,
		&event.MaxTicketsPerPurchase, &event.MaterialChangedAt,
		&event.CreatedAt, &event.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: publish event: %w", ErrConflict)
		}
		return nil, fmt.Errorf("store: publish event: %w", err)
	}

	return event, nil
}

func (s *EventsStore) Cancel(ctx context.Context, id string) (*Event, error) {
	query := `
    UPDATE events
    SET status = 'cancelled'
    WHERE id = $1 AND status IN ('draft', 'published', 'sold_out')
    RETURNING id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
  `

	event := &Event{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&event.ID, &event.OrganizerID, &event.Name, &event.Description,
		&event.Location, &event.Status, &event.StartsAt, &event.EndsAt,
		&event.Capacity, &event.CancellationAllowed, &event.CancellationHoursBefore,
		&event.MaxTicketsPerPurchase, &event.MaterialChangedAt, &event.CreatedAt, &event.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: cancel event: %w", ErrConflict)
		}
		return nil, fmt.Errorf("store: cancel event: %w", err)
	}

	return event, nil
}

func (s *EventsStore) SetStatus(ctx context.Context, id, from, to string) error {
	query := `
    UPDATE events
    SET status = $3
    WHERE id = $1 AND status = $2
  `

	if _, err := s.pool.Exec(ctx, query, id, from, to); err != nil {
		return fmt.Errorf("store: set event status: %w", err)
	}

	return nil
}

func (s *EventsStore) GetByID(ctx context.Context, id string) (*Event, error) {
	query := `
    SELECT id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
    FROM events
    WHERE id = $1
  `

	event := &Event{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&event.ID, &event.OrganizerID, &event.Name, &event.Description,
		&event.Location, &event.Status, &event.StartsAt, &event.EndsAt,
		&event.Capacity, &event.CancellationAllowed, &event.CancellationHoursBefore,
		&event.MaxTicketsPerPurchase, &event.MaterialChangedAt, &event.CreatedAt, &event.UpdatedAt,
	)

	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("store: get event by id: %w", ErrNotFound)
		default:
			return nil, fmt.Errorf("store: get event by id: %w", err)
		}
	}

	return event, nil
}

func (s *EventsStore) GetAllPublished(
	ctx context.Context,
	cursor *cursor.Cursor,
	limit int,
) ([]*Event, error) {
	query := `
    SELECT id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
    FROM events
    WHERE status = 'published'
  `

	var args []any
	if cursor != nil {
		query += `AND (starts_at, id) > ($1, $2)`
		args = append(args, cursor.Timestamp, cursor.ID)
	}

	query += `ORDER BY starts_at ASC, id ASC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: get published events: %w", err)
	}
	defer rows.Close()

	events, err := pgx.CollectRows(rows, pgx.RowToAddrOf[Event])
	if err != nil {
		return nil, fmt.Errorf("store: collect published events: %w", err)
	}

	return events, nil
}

func (s *EventsStore) EndAllExpired(ctx context.Context) ([]*Event, error) {
	query := `
    UPDATE events SET status = 'ended'
    WHERE status IN ('published', 'sold_out')
      AND ends_at < NOW()
    RETURNING id, organizer_id, name, description, location, status, starts_at,
    ends_at, capacity, cancellation_allowed, cancellation_hours_before,
    max_tickets_per_purchase, material_changed_at, created_at, updated_at
  `

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: end all expired events: %w", err)
	}
	defer rows.Close()

	events, err := pgx.CollectRows(rows, pgx.RowToAddrOf[Event])
	if err != nil {
		return nil, fmt.Errorf("store: end all expired events: %w", err)
	}

	return events, nil
}
