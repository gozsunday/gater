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

type TicketsStore struct {
	pool *pgxpool.Pool
}

type Ticket struct {
	ID          uuid.UUID  `json:"id"`
	PurchaseID  uuid.UUID  `json:"purchase_id"`
	TierID      uuid.UUID  `json:"tier_id"`
	QRToken     string     `json:"qr_token"`
	Status      string     `json:"status"`
	CheckedInAt *time.Time `json:"checked_in_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type TicketCheckIn struct {
	Ticket        *Ticket
	TierName      string
	AttendeeName  string
	AttendeeEmail string
}

func (s *TicketsStore) CheckIn(ctx context.Context, ticketID, eventID uuid.UUID) (*TicketCheckIn, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeoutDuration)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: check in tickets: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// lock the query row, and fetch everything in one query
	// locking only the tickets to avoid blocking queries on the other tables involved
	const lockTicketAndQuery = `
    SELECT t.id, t.purchase_id, t.tier_id, t.qr_token, t.status, t.checked_in_at,
          ti.event_id, ti.name,
          u.name, u.email
    FROM tickets t
    JOIN ticket_tiers ti ON ti.id = t.tier_id
    JOIN purchases p ON p.id = t.purchase_id
    JOIN users u ON u.id = p.user_id
    WHERE t.id = $1
    FOR UPDATE OF t
  `

	ticketCheckIn := &TicketCheckIn{Ticket: &Ticket{}}
	var tierEventID uuid.UUID

	err = tx.QueryRow(ctx, lockTicketAndQuery, ticketID).Scan(
		&ticketCheckIn.Ticket.ID, &ticketCheckIn.Ticket.PurchaseID, &ticketCheckIn.Ticket.TierID,
		&ticketCheckIn.Ticket.QRToken, &ticketCheckIn.Ticket.Status,
		&ticketCheckIn.Ticket.CheckedInAt, &tierEventID, &ticketCheckIn.TierName,
		&ticketCheckIn.AttendeeName, &ticketCheckIn.AttendeeEmail,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: check in tickets: lock ticket: %w", err)
	}

	switch {
	case ticketCheckIn.Ticket.Status == "used":
		return nil, ErrAlreadyCheckedIn
	case ticketCheckIn.Ticket.Status == "cancelled":
		return nil, ErrTicketCancelled
	case tierEventID.String() != eventID.String():
		return nil, ErrWrongEvent
	}

	// flip ticket status
	const flipTicket = `
    UPDATE tickets
    SET status = 'used', checked_in_at = NOW()
    WHERE id = $1
    RETURNING status, created_at, updated_at, checked_in_at
  `
	err = tx.QueryRow(ctx, flipTicket, ticketCheckIn.Ticket.ID).Scan(
		&ticketCheckIn.Ticket.Status, &ticketCheckIn.Ticket.CreatedAt,
		&ticketCheckIn.Ticket.UpdatedAt, &ticketCheckIn.Ticket.CheckedInAt,
	)
	if err != nil {
		return nil, fmt.Errorf("store: check in tickets: flip ticket: %w", err)
	}

	// commit transaction
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: check in tickets: commit: %w", err)
	}

	return ticketCheckIn, nil
}
