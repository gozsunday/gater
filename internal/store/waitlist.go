package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WaitlistStore struct {
	pool *pgxpool.Pool
}

type WaitlistEntry struct {
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"user_id"`
	TierID     uuid.UUID  `json:"tier_id"`
	Status     string     `json:"status"`
	NotifiedAt *time.Time `json:"notified_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type WaitlistSummary struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status"`
	NotifiedAt *time.Time `json:"notified_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	User       struct {
		ID    uuid.UUID `json:"id"`
		Name  string    `json:"name"`
		Email string    `json:"email"`
	} `json:"user"`
	Tier struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"tier"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *WaitlistStore) Create(ctx context.Context, entry *WaitlistEntry) error {
	query := `
		INSERT INTO waitlist_entries (user_id, tier_id)
		VALUES ($1, $2)
		RETURNING id, user_id, tier_id, status, notified_at, expires_at,
		created_at, updated_at
	`

	err := s.pool.QueryRow(
		ctx, query, entry.UserID, entry.TierID,
	).Scan(
		&entry.ID, &entry.UserID, &entry.TierID, &entry.Status,
		&entry.NotifiedAt, &entry.ExpiresAt, &entry.CreatedAt, &entry.UpdatedAt,
	)
	if err != nil {
		// postgres unique_violation: this user already has a row for this
		// tier in ANY state (waiting/notified/expired/purchased)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("store: create waitlist entry: %w", ErrConflict)
		}
		return fmt.Errorf("store: create waitlist entry: %w", err)
	}

	return nil
}

func (s *WaitlistStore) DeleteByUserAndTier(ctx context.Context, userID, tierID string) error {
	query := `
		DELETE FROM waitlist_entries
		WHERE user_id = $1 AND tier_id = $2
	`

	ct, err := s.pool.Exec(ctx, query, userID, tierID)
	if err != nil {
		return fmt.Errorf("store: delete waitlist entry: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store: delete waitlist entry: %w", ErrNotFound)
	}

	return nil
}

func (s *WaitlistStore) ListByEvent(ctx context.Context, eventID string) ([]WaitlistSummary, error) {
	query := `
		SELECT w.id, w.status, w.notified_at, w.expires_at,
		u.id, u.name, u.email,
		t.id, t.name,
		w.created_at
		FROM waitlist_entries w
		JOIN users u ON u.id = w.user_id
		JOIN ticket_tiers t ON t.id = w.tier_id
		WHERE t.event_id = $1
		ORDER BY w.created_at ASC
	`

	rows, err := s.pool.Query(ctx, query, eventID)
	if err != nil {
		return nil, fmt.Errorf("store: list waitlist by event: %w", err)
	}
	defer rows.Close()

	// non-nil so an empty waitlist marshals as [] not null
	summaries := make([]WaitlistSummary, 0)

	for rows.Next() {
		var summary WaitlistSummary
		err := rows.Scan(
			&summary.ID, &summary.Status, &summary.NotifiedAt, &summary.ExpiresAt,
			&summary.User.ID, &summary.User.Name, &summary.User.Email,
			&summary.Tier.ID, &summary.Tier.Name,
			&summary.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("store: list waitlist by event: %w", err)
		}
		summaries = append(summaries, summary)
	}
	// get errors the iteration itself hit
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list waitlist by event: %w", err)
	}

	return summaries, nil
}

func (s *WaitlistStore) ExpireReservations(ctx context.Context) ([]*WaitlistEntry, error) {
	query := `
    UPDATE waitlist_entries SET status = 'expired'
    WHERE status = 'notified' AND expires_at < NOW()
    RETURNING id, user_id, tier_id, status, notified_at, expires_at,
		created_at, updated_at
  `

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: expire waitlist reservations: %w", err)
	}
	defer rows.Close()

	waitlistEntries, err := pgx.CollectRows(rows, pgx.RowToAddrOf[WaitlistEntry])
	if err != nil {
		return nil, fmt.Errorf("store: expire waitlist reservations: %w", err)
	}

	return waitlistEntries, nil
}

func (s *WaitlistStore) NotifyNextWaiting(
	ctx context.Context,
	tierID uuid.UUID,
) (*WaitlistEntry, *User, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeoutDuration)
	defer cancel()

	// init tx
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("store: notify next waiting: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// get waitlist entry and associated user; lock tier
	const lockWaitlistEntry = `
    SELECT w.id, w.user_id, w.tier_id, w.status, w.notified_at, 
    w.expires_at, w.created_at, w.updated_at,
    u.id, u.name, u.email, u.password_hash, u.email_verified,
    u.image, u.role, u.created_at, u.updated_at,
    t.name, e.name
    FROM waitlist_entries w
    JOIN users u ON u.id = w.user_id
    JOIN ticket_tiers t ON t.id = w.tier_id
    JOIN events e ON e.id = t.event_id
    WHERE w.tier_id = $1 AND w.status = 'waiting'
    ORDER BY w.created_at ASC
    LIMIT 1
    FOR UPDATE OF w
  `

	waitlistEntry := &WaitlistEntry{}
	user := &User{}
	var tierName, eventName string
	err = tx.QueryRow(ctx, lockWaitlistEntry, tierID).Scan(
		&waitlistEntry.ID, &waitlistEntry.UserID, &waitlistEntry.TierID,
		&waitlistEntry.Status, &waitlistEntry.NotifiedAt, &waitlistEntry.ExpiresAt,
		&waitlistEntry.CreatedAt, &waitlistEntry.UpdatedAt,
		&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.EmailVerified,
		&user.Image, &user.Role, &user.CreatedAt, &user.UpdatedAt,
		&tierName, &eventName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, "", "", fmt.Errorf("store: notify next waiting: %w", ErrNotFound)
		}
		return nil, nil, "", "", fmt.Errorf("store: notify next waiting: get entry: %w", err)
	}

	notifiedAt := time.Now()
	expiresAt := notifiedAt.Add(24 * time.Hour)

	// update as notified
	const setNotified = `
    UPDATE waitlist_entries
    SET status = 'notified', notified_at = $2, expires_at = $3
    WHERE id = $1 AND status = 'waiting'
    RETURNING status, notified_at, expires_at, updated_at
  `

	err = tx.QueryRow(
		ctx, setNotified, waitlistEntry.ID,
		notifiedAt, expiresAt,
	).Scan(
		&waitlistEntry.Status, &waitlistEntry.NotifiedAt,
		&waitlistEntry.ExpiresAt, &waitlistEntry.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, "", "", fmt.Errorf("store: notify next waiting: %w", ErrNotFound)
		}
		return nil, nil, "", "", fmt.Errorf("store: notify next waiting: set as notified: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, "", "", fmt.Errorf("store: notify next waiting: commit: %w", err)
	}

	return waitlistEntry, user, tierName, eventName, nil
}

func (s *WaitlistStore) DeleteByEvent(ctx context.Context, eventID string) error {
	query := `
		DELETE FROM waitlist_entries
		WHERE tier_id IN (
			SELECT id FROM ticket_tiers WHERE event_id = $1
		)
	`

	if _, err := s.pool.Exec(ctx, query, eventID); err != nil {
		return fmt.Errorf("store: delete waitlist by event: %w", err)
	}

	return nil
}
