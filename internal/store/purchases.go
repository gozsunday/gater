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

type PurchasesStore struct {
	pool *pgxpool.Pool
}

type Purchase struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	TierID    uuid.UUID `json:"tier_id"`
	Quantity  int       `json:"quantity"`
	Total     int       `json:"total"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// tickets are excluded entirely from the purchase summary, because
// QR tokens are bulky and belong behind /purchases/{id}.
type PurchaseSummary struct {
	ID       uuid.UUID `json:"id"`
	Quantity int       `json:"quantity"`
	Total    int       `json:"total"`
	Status   string    `json:"status"`
	Event    struct {
		ID       uuid.UUID `json:"id"`
		Name     string    `json:"name"`
		StartsAt time.Time `json:"starts_at"`
	} `json:"event"`
	Tier struct {
		ID    uuid.UUID `json:"id"`
		Name  string    `json:"name"`
		Price int       `json:"price"`
	} `json:"tier"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *PurchasesStore) ListByUser(
	ctx context.Context,
	userID string,
	limit, offset int,
) ([]PurchaseSummary, error) {
	query := `
		SELECT p.id, p.quantity, p.total, p.status, p.created_at,
		e.id, e.name, e.starts_at,
		t.id, t.name, t.price
		FROM purchases p
		JOIN ticket_tiers t ON t.id = p.tier_id
		JOIN events e ON e.id = t.event_id
		WHERE p.user_id = $1
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := s.pool.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// non-nil so an empty history marshals as [] not null
	summaries := make([]PurchaseSummary, 0)

	for rows.Next() {
		var summary PurchaseSummary
		err := rows.Scan(
			&summary.ID, &summary.Quantity, &summary.Total, &summary.Status, &summary.CreatedAt,
			&summary.Event.ID, &summary.Event.Name, &summary.Event.StartsAt,
			&summary.Tier.ID, &summary.Tier.Name, &summary.Tier.Price,
		)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	// get errors the iteration itself hit
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return summaries, nil
}

func (s *PurchasesStore) CountByUser(ctx context.Context, userID string) (int, error) {
	// the DB index `idx_purchases_user_id` makes this query cheap
	query := `
		SELECT COUNT(*)
		FROM purchases
		WHERE user_id = $1
	`

	var count int
	err := s.pool.QueryRow(ctx, query, userID).Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

func (s *PurchasesStore) ListConfirmedBuyersByEvent(
	ctx context.Context,
	eventID string,
) ([]*User, error) {
	query := `
		SELECT DISTINCT u.id, u.name, u.email, u.password_hash, u.email_verified,
		u.image, u.role, u.created_at, u.updated_at
		FROM purchases p
		JOIN ticket_tiers t ON t.id = p.tier_id
		JOIN users u ON u.id = p.user_id
		WHERE t.event_id = $1 AND p.status = 'confirmed'
		ORDER BY u.email ASC
	`

	rows, err := s.pool.Query(ctx, query, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buyers, err := pgx.CollectRows(rows, pgx.RowToAddrOf[User])
	if err != nil {
		return nil, err
	}

	return buyers, nil
}

func (s *PurchasesStore) SumConfirmedQuantityByEvent(ctx context.Context, eventID string) (int, error) {
	// COALESCE ensures 0 is returned if no sales has been made (SUM over zero rows is NULL)
	query := `
		SELECT COALESCE(SUM(p.quantity), 0)
		FROM purchases p
		JOIN ticket_tiers t ON t.id = p.tier_id
		WHERE t.event_id = $1 AND p.status = 'confirmed'
	`

	var sum int
	err := s.pool.QueryRow(ctx, query, eventID).Scan(&sum)
	if err != nil {
		return 0, err
	}

	return sum, nil
}

func (s *PurchasesStore) GetByID(ctx context.Context, id, userID string) (*Purchase, error) {
	query := `
		SELECT id, user_id, tier_id, quantity, total, status,
		created_at, updated_at
		FROM purchases
		WHERE id = $1 AND user_id = $2
	`

	purchase := &Purchase{}
	err := s.pool.QueryRow(ctx, query, id, userID).Scan(
		&purchase.ID, &purchase.UserID, &purchase.TierID, &purchase.Quantity,
		&purchase.Total, &purchase.Status, &purchase.CreatedAt, &purchase.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return purchase, nil
}

func (s *PurchasesStore) ListTicketsByPurchase(ctx context.Context, purchaseID string) ([]Ticket, error) {
	query := `
		SELECT id, purchase_id, tier_id, qr_token, status,
		checked_in_at, created_at, updated_at
		FROM tickets
		WHERE purchase_id = $1
		ORDER BY created_at ASC
	`

	rows, err := s.pool.Query(ctx, query, purchaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// create a slice of Ticket from the rows gotten from the DB query
	tickets, err := pgx.CollectRows(rows, pgx.RowTo[Ticket])
	if err != nil {
		return nil, err
	}

	return tickets, nil
}

func (s *PurchasesStore) Create(
	ctx context.Context,
	purchase *Purchase,
	tickets []Ticket,
) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeoutDuration)
	defer cancel()

	// init tx
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: create purchase: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// lock the tier row; concurrent buyers queue until COMMIT
	const lockTier = `
		SELECT price, remaining, status, event_id
		FROM ticket_tiers WHERE id = $1
		FOR UPDATE
	`
	var price, remaining int
	var tierStatus string
	var eventID uuid.UUID
	err = tx.QueryRow(ctx, lockTier, purchase.TierID).Scan(
		&price, &remaining, &tierStatus, &eventID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: create purchase: lock tier: %w", err)
	}

	// load the event's sale rules
	const loadEventRules = `
		SELECT status, max_tickets_per_purchase
		FROM events
		WHERE id = $1
	`
	var eventStatus string
	var maxTicketsPerPurchase int
	err = tx.QueryRow(ctx, loadEventRules, eventID).Scan(&eventStatus, &maxTicketsPerPurchase)
	// if tier already exists then the event it belongs to definitely exists
	// so no pgx.ErrNoRows error to check here
	if err != nil {
		return fmt.Errorf("store: create purchase: load event rules: %w", err)
	}

	switch {
	case eventStatus != "published":
		return ErrEventNotPublished
	case purchase.Quantity > maxTicketsPerPurchase:
		return ErrExceedsMaxPerPurchase
	case tierStatus != "available" || remaining < purchase.Quantity:
		return ErrInsufficientRemaining
	}

	// take the inventory
	const decrementTier = `
		UPDATE ticket_tiers
		SET remaining = remaining - $2,
				status = CASE WHEN remaining - $2 = 0 THEN 'sold_out' ELSE 'available' END
		WHERE id = $1
	`
	if _, err := tx.Exec(ctx, decrementTier, purchase.TierID, purchase.Quantity); err != nil {
		return fmt.Errorf("store: create purchase: decrement tier: %w", err)
	}

	// record the purchase
	purchase.Total = price * purchase.Quantity

	const insertPurchase = `
		INSERT INTO purchases (id, user_id, tier_id, quantity, total)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING status, created_at, updated_at
	`
	// the purchase ID is supplied by the handler (QR tokens embed it)
	err = tx.QueryRow(
		ctx, insertPurchase, purchase.ID, purchase.UserID,
		purchase.TierID, purchase.Quantity, purchase.Total,
	).Scan(&purchase.Status, &purchase.CreatedAt, &purchase.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: create purchase: insert purchase: %w", err)
	}

	// mint one ticket per unit
	// pgx.Batch queues N inserts and ships them in ONE network round trip
	// qr_token was signed by the handler; status defaults to 'unused'
	batch := &pgx.Batch{}
	for i := range tickets {
		batch.Queue(`
			INSERT INTO tickets (id, purchase_id, tier_id, qr_token)
			VALUES ($1, $2, $3, $4)
			RETURNING status, created_at, updated_at
		`, tickets[i].ID, purchase.ID, purchase.TierID, tickets[i].QRToken).
			QueryRow(func(row pgx.Row) error {
				err := row.Scan(&tickets[i].Status, &tickets[i].CreatedAt, &tickets[i].UpdatedAt)
				if err != nil {
					return fmt.Errorf("scan ticket %d: %w", i, err)
				}
				return nil
			})
	}

	// Close completes the batch
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("store: create purchase: insert tickets: %w", err)
	}

	// check if there's any stock left for the event
	var anyAvailable bool
	const anyStockLeft = `
		SELECT EXISTS (
			SELECT 1 FROM ticket_tiers WHERE event_id = $1 AND remaining > 0
		)
	`
	if err := tx.QueryRow(ctx, anyStockLeft, eventID).Scan(&anyAvailable); err != nil {
		return fmt.Errorf("store: create purchase: check stock: %w", err)
	}
	// if there's no available tier, flip from published to sold_out
	if !anyAvailable {
		const sellOutEvent = `
			UPDATE events SET status = 'sold_out'
			WHERE id = $1 AND status = 'published'
		`
		if _, err := tx.Exec(ctx, sellOutEvent, eventID); err != nil {
			return fmt.Errorf("store: create purchase: flip event: %w", err)
		}
	}

	// commit transaction
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: create purchase: commit: %w", err)
	}

	return nil
}

func (s *PurchasesStore) Cancel(
	ctx context.Context,
	id, userID string,
) (*Purchase, bool, error) {
	wasSoldOut := false

	ctx, cancel := context.WithTimeout(ctx, queryTimeoutDuration)
	defer cancel()

	// init tx
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// lock the purchase row
	const lockPurchase = `
    SELECT id, user_id, tier_id, quantity, total, status, created_at
    FROM purchases WHERE id = $1 AND user_id = $2
    FOR UPDATE
  `

	purchase := &Purchase{}
	err = tx.QueryRow(ctx, lockPurchase, id, userID).Scan(
		&purchase.ID, &purchase.UserID, &purchase.TierID, &purchase.Quantity,
		&purchase.Total, &purchase.Status, &purchase.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, wasSoldOut, ErrNotFound
		}
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: lock purchase: %w", err)
	}
	// if purchase is already cancelled, return error
	if purchase.Status != "confirmed" {
		return nil, wasSoldOut, ErrAlreadyCancelled
	}

	// load the tier + event rules
	const loadEventAndTierRules = `
    SELECT t.event_id, t.status, t.remaining, 
      e.status, e.starts_at, e.cancellation_allowed, 
      e.cancellation_hours_before, e.material_changed_at
    FROM ticket_tiers t JOIN events e ON e.id = t.event_id
    WHERE t.id = $1
  `
	var eventID uuid.UUID
	var remaining, cancellationHoursBefore int
	var eventStatus string
	var cancellationAllowed bool
	var startsAt time.Time
	var materialChangedAt *time.Time
	var tierStatus string
	err = tx.QueryRow(ctx, loadEventAndTierRules, purchase.TierID).Scan(
		&eventID, &tierStatus, &remaining, &eventStatus, &startsAt,
		&cancellationAllowed, &cancellationHoursBefore, &materialChangedAt,
	)
	// if the purchase that tierId is gotten from already exists,
	// then the tier and the event definitely exists
	// so no pgx.ErrNoRows error to check here
	if err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: load tier + event rules: %w", err)
	}

	// record sold out state
	if tierStatus == "sold_out" {
		wasSoldOut = true
	}

	// policy checks. hard gates first: once the event has started it's over
	// regardless of anything else. this also caps the grace window below at
	// the event start, so a late material change can't extend past it
	now := time.Now()
	switch {
	case !now.Before(startsAt):
		return nil, wasSoldOut, ErrEventStarted
	case !cancellationAllowed:
		return nil, wasSoldOut, ErrCancellationNotAllowed
	}

	// two ways to be inside a valid cancellation window:
	//   normal — more than cancellation_hours_before remain until start
	//   grace  — a confirmed material change landed recently; buyers who had
	//            their deal changed get an extended right to walk away
	windowClosed := now.After(startsAt.Add(-time.Duration(cancellationHoursBefore) * time.Hour))
	graceOpen := materialChangedAt != nil &&
		now.Before(materialChangedAt.Add(materialChangeGracePeriod))
	if windowClosed && !graceOpen {
		return nil, wasSoldOut, ErrOutsideCancellationWindow
	}

	// flip purchase status from confirmed to cancelled
	const flipPurchase = `
    UPDATE purchases SET status = 'cancelled'
    WHERE id = $1 RETURNING status, updated_at
  `
	err = tx.QueryRow(ctx, flipPurchase, purchase.ID).Scan(
		&purchase.Status, &purchase.UpdatedAt,
	)
	if err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: flip purchase: %w", err)
	}

	// flip the purchase tickets
	const flipTickets = `
    UPDATE tickets SET status = 'cancelled'
    WHERE purchase_id = $1
  `
	if _, err := tx.Exec(ctx, flipTickets, purchase.ID); err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: flip tickets: %w", err)
	}

	// restore ticket tier inventory
	const restoreTierInventory = `
    UPDATE ticket_tiers
    SET remaining = remaining + $2, status = 'available'
    WHERE id = $1
  `
	_, err = tx.Exec(ctx, restoreTierInventory, purchase.TierID, purchase.Quantity)
	if err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: flip tier inventory: %w", err)
	}

	const flipEventStatus = `
    UPDATE events SET status = 'published'
    WHERE id = $1 AND status = 'sold_out'
  `
	if _, err := tx.Exec(ctx, flipEventStatus, eventID); err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: flip event status: %w", err)
	}

	// commit transaction
	if err := tx.Commit(ctx); err != nil {
		return nil, wasSoldOut, fmt.Errorf("store: cancel purchase: commit: %w", err)
	}

	return purchase, wasSoldOut, nil
}

func (s *PurchasesStore) CancelByEvent(ctx context.Context, eventID string) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeoutDuration)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: cancel by event: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// flip confirmed purchases for this event
	const flipPurchases = `
		UPDATE purchases p
		SET status = 'cancelled'
		FROM ticket_tiers t
		WHERE t.id = p.tier_id
		  AND t.event_id = $1
		  AND p.status = 'confirmed'
	`
	if _, err := tx.Exec(ctx, flipPurchases, eventID); err != nil {
		return fmt.Errorf("store: cancel by event: flip purchases: %w", err)
	}

	// flip tickets whose purchases were just cancelled
	// (or were already cancelled)
	// using a sub-select keeps it single-statement and
	// idempotent if this runs twice
	const flipTickets = `
		UPDATE tickets SET status = 'cancelled'
		WHERE purchase_id IN (
			SELECT p.id FROM purchases p
			JOIN ticket_tiers t ON t.id = p.tier_id
			WHERE t.event_id = $1
		)
		AND status != 'cancelled'
	`
	if _, err := tx.Exec(ctx, flipTickets, eventID); err != nil {
		return fmt.Errorf("store: cancel by event: flip tickets: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: cancel by event: commit: %w", err)
	}

	return nil
}

func (s *PurchasesStore) HasConfirmedPurchase(ctx context.Context, userID, tierID string) (bool, error) {
	query := `
		SELECT EXISTS (
			SELECT 1 FROM purchases
			WHERE user_id = $1 AND tier_id = $2 AND status = 'confirmed'
		)
	`

	var exists bool
	err := s.pool.QueryRow(ctx, query, userID, tierID).Scan(&exists)
	if err != nil {
		return false, err
	}

	return exists, nil
}
