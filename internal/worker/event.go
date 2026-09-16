package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/mailer"
	"github.com/gozsunday/gater/internal/store"
	"github.com/hibiken/asynq"
)

type NotifyBuyersUpdatedPayload struct {
	EventID           uuid.UUID         `json:"event_id"`
	EventName         string            `json:"event_name"`
	ChangedFields     map[string]string `json:"changed_fields"`
	MaterialChangedAt time.Time         `json:"material_changed_at"`
}

type NotifyBuyersCancelledPayload struct {
	EventID     uuid.UUID `json:"event_id"`
	EventName   string    `json:"event_name"`
	CancelledAt time.Time `json:"cancelled_at"`
	Buyers      []struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		ID    string `json:"id"`
	} `json:"buyers"`
}

func NewNotifyBuyersUpdatedTask(
	eventID uuid.UUID,
	eventName string,
	changedFields map[string]string,
	materialChangedAt time.Time,
) (*asynq.Task, error) {
	payload, err := json.Marshal(NotifyBuyersUpdatedPayload{
		EventID:           eventID,
		EventName:         eventName,
		ChangedFields:     changedFields,
		MaterialChangedAt: materialChangedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("worker: notify buyers updated: marshal: %w", err)
	}

	return asynq.NewTask(TypeNotifyBuyersUpdated, payload), nil
}

func HandleNotifyBuyersUpdated(s store.Store, m mailer.Mailer, l *slog.Logger) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		var p NotifyBuyersUpdatedPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("worker: handle notify buyers updated: unmarshal: %w", err)
		}

		// resolve affected buyers at execution time
		buyers, err := s.Purchases.ListConfirmedBuyersByEvent(ctx, p.EventID.String())
		if err != nil {
			return fmt.Errorf("worker: handle notify buyers updated: list buyers: %w", err)
		}

		for _, buyer := range buyers {
			err := m.SendEventUpdatedNotification(
				ctx,
				[]string{buyer.Email},
				buyer.Name,
				p.EventName,
				p.ChangedFields,
				p.MaterialChangedAt,
			)
			if err != nil {
				l.Error(
					"failed to send event updated notification",
					"error", err,
					"event_id", p.EventID,
					"user_id", buyer.ID,
					"email", buyer.Email,
				)
				continue
			}
			l.Info(
				"sent event updated notification",
				"event_id", p.EventID,
				"user_id", buyer.ID,
			)
		}

		return nil
	}
}

func NewNotifyBuyersCancelledTask(
	eventID uuid.UUID,
	eventName string,
	cancelledAt time.Time,
	buyers []*store.User,
) (*asynq.Task, error) {
	// snapshot buyer identity into the task so the flip to 'cancelled' that
	// happens in the handler doesn't hide them from the worker's later lookup
	bs := make([]struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		ID    string `json:"id"`
	}, 0, len(buyers))
	for _, b := range buyers {
		bs = append(bs, struct {
			Email string `json:"email"`
			Name  string `json:"name"`
			ID    string `json:"id"`
		}{Email: b.Email, Name: b.Name, ID: b.ID.String()})
	}
	payload, err := json.Marshal(NotifyBuyersCancelledPayload{
		EventID:     eventID,
		EventName:   eventName,
		CancelledAt: cancelledAt,
		Buyers:      bs,
	})
	if err != nil {
		return nil, fmt.Errorf("worker: notify buyers cancelled: marshal: %w", err)
	}

	return asynq.NewTask(TypeNotifyBuyersCancelled, payload), nil
}

func HandleNotifyBuyersCancelled(m mailer.Mailer, l *slog.Logger) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		var p NotifyBuyersCancelledPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("worker: handle notify buyers cancelled: unmarshal: %w", err)
		}

		// buyers are snapshotted in the task at enqueue time (before the
		// synchronous CancelByEvent flips them to 'cancelled'), so we must
		// not re-query for confirmed buyers here

		for _, buyer := range p.Buyers {
			buyerID, _ := uuid.Parse(buyer.ID)
			err := m.SendEventCancelledNotification(
				ctx,
				[]string{buyer.Email},
				buyer.Name,
				p.EventName,
				p.CancelledAt,
			)
			if err != nil {
				l.Error(
					"failed to send event cancelled notification",
					"error", err,
					"event_id", p.EventID,
					"user_id", buyerID,
					"email", buyer.Email,
				)
				continue
			}
			l.Info(
				"sent event cancelled notification",
				"event_id", p.EventID,
				"user_id", buyerID,
			)
		}

		return nil
	}
}
