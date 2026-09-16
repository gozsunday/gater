package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/mailer"
	"github.com/gozsunday/gater/internal/store"
	"github.com/hibiken/asynq"
)

type NotifyWaitlistEntryPayload struct {
	TierID uuid.UUID
}

func NewNotifyWaitlistEntryTask(tierID uuid.UUID) (*asynq.Task, error) {
	payload, err := json.Marshal(NotifyWaitlistEntryPayload{TierID: tierID})
	if err != nil {
		return nil, fmt.Errorf("worker: notify waitlist entry: marshal: %w", err)
	}

	return asynq.NewTask(TypeNotifyWaitlistEntry, payload), nil
}

func HandleNotifyNextWaiting(s store.Store, m mailer.Mailer) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		var p NotifyWaitlistEntryPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("worker: handle notify next waiting: unmarshal: %w", err)
		}

		// set notified
		waitlistEntry, user, tierName, eventName, err := s.Waitlist.NotifyNextWaiting(ctx, p.TierID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf(
					"worker: handle notify next waiting: no waiting entries: %w",
					asynq.SkipRetry,
				)
			}
			return fmt.Errorf("worker: handle notify next waiting: set notified: %w", err)
		}

		if waitlistEntry.ExpiresAt == nil {
			return fmt.Errorf("nil expiry for %s: %w", waitlistEntry.ID, asynq.SkipRetry)
		}

		// send email
		err = m.SendWaitlistNotification(
			ctx,
			[]string{user.Email},
			user.Name,
			tierName, eventName,
			*waitlistEntry.ExpiresAt,
		)
		if err != nil {
			return fmt.Errorf("worker: handle notify next waiting: send email: %w", err)
		}

		return nil
	}
}
