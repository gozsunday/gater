package worker

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gozsunday/gater/internal/store"
	"github.com/hibiken/asynq"
)

func HandleEndExpiredEvents(s store.Store, l *slog.Logger) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		expiredEvents, err := s.Events.EndAllExpired(ctx)
		if err != nil {
			return fmt.Errorf("worker: handle end expired events: %w", err)
		}

		l.Info("expired events sweep", "count", len(expiredEvents))
		return nil
	}
}

func HandleExpireWaitlistReservations(
	s store.Store,
	c *asynq.Client,
	l *slog.Logger,
) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		waitlistEntries, err := s.Waitlist.ExpireReservations(ctx)
		if err != nil {
			return fmt.Errorf("worker: handle expire reservations: %w", err)
		}

		for _, w := range waitlistEntries {
			notifyEntryTask, err := NewNotifyWaitlistEntryTask(w.TierID)
			if err != nil {
				l.Error(
					fmt.Sprintf("failed to create %s task", TypeNotifyWaitlistEntry),
					"error", err,
					"tier_id", w.TierID,
				)
				continue
			}

			taskInfo, err := c.Enqueue(notifyEntryTask)
			if err != nil {
				l.Error(
					fmt.Sprintf("failed to enqueue %s task", TypeNotifyWaitlistEntry),
					"error", err,
					"tier_id", w.TierID,
				)
				continue
			}
			l.Info(
				fmt.Sprintf("enqueued %s task", TypeNotifyWaitlistEntry),
				"id", taskInfo.ID,
				"queue", taskInfo.Queue,
			)
		}

		return nil
	}
}
