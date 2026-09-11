package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConflict                  = errors.New("resource already exists")
	ErrNotFound                  = errors.New("resource not found")
	ErrWrongEvent                = errors.New("ticket does not belong to this event")
	ErrEventStarted              = errors.New("event has already started")
	ErrTicketCancelled           = errors.New("ticket has been cancelled")
	ErrAlreadyCancelled          = errors.New("purchase has already been cancelled")
	ErrAlreadyCheckedIn          = errors.New("ticket has already been checked in")
	ErrEventNotPublished         = errors.New("event is not open for ticket sales")
	ErrInsufficientRemaining     = errors.New("not enough tickets remaining")
	ErrExceedsMaxPerPurchase     = errors.New("quantity exceeds the maximum tickets per purchase")
	ErrCancellationNotAllowed    = errors.New("event does not allow cancellations")
	ErrOutsideCancellationWindow = errors.New("cancellation window has closed")
)

var queryTimeoutDuration = time.Second * 5

// materialChangeGracePeriod is how long buyers keep an extended right to
// cancel after an organizer lands a confirmed material change on their
// event. Capped at the event start.
const materialChangeGracePeriod = time.Hour * 72

const (
	RoleOrganizer string = "organizer"
	RoleAttendee  string = "attendee"
)

type Store struct {
	Users         *UserStore
	Sessions      *SessionStore
	Verifications *VerificationStore
	OAuthAccounts *OAuthStore
	Events        *EventsStore
	Tiers         *TiersStore
	Purchases     *PurchasesStore
	Waitlist      *WaitlistStore
	Tickets       *TicketsStore
}

func New(pool *pgxpool.Pool) Store {
	return Store{
		Users:         &UserStore{pool},
		Sessions:      &SessionStore{pool},
		Verifications: &VerificationStore{pool},
		OAuthAccounts: &OAuthStore{pool},
		Events:        &EventsStore{pool},
		Tiers:         &TiersStore{pool},
		Purchases:     &PurchasesStore{pool},
		Waitlist:      &WaitlistStore{pool},
		Tickets:       &TicketsStore{pool},
	}
}
