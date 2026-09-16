package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gozsunday/gater/internal/mailer"
	"github.com/hibiken/asynq"
)

type SendVerificationPayload struct {
	To    []string
	Name  string
	Token string
}

type SendPasswordResetPayload struct {
	To    []string
	Name  string
	Token string
}

func NewSendVerificationTask(
	to []string,
	name, token string,
) (*asynq.Task, error) {
	payload, err := json.Marshal(SendVerificationPayload{
		To:    to,
		Name:  name,
		Token: token,
	})
	if err != nil {
		return nil, fmt.Errorf("worker: send email: marshal: %w", err)
	}

	return asynq.NewTask(TypeSendVerificationEmail, payload), nil
}

func HandleSendVerificationTask(m mailer.Mailer) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		var p SendVerificationPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("worker: handle send email: unmarshal: %w", err)
		}

		if err := m.SendVerificationEmail(ctx, p.To, p.Name, p.Token); err != nil {
			return fmt.Errorf("worker: handle send email: %w", err)
		}

		return nil
	}
}

func NewSendPwdResetTask(
	to []string,
	name, token string,
) (*asynq.Task, error) {
	payload, err := json.Marshal(SendPasswordResetPayload{
		To:    to,
		Name:  name,
		Token: token,
	})
	if err != nil {
		return nil, fmt.Errorf("worker: send pwd reset: marshal: %w", err)
	}

	return asynq.NewTask(TypeSendPasswordResetEmail, payload), nil
}

func HandleSendPwdResetTask(m mailer.Mailer) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		var p SendPasswordResetPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("worker: handle send pwd reset: unmarshal: %w", err)
		}

		if err := m.SendPasswordResetEmail(ctx, p.To, p.Name, p.Token); err != nil {
			return fmt.Errorf("worker: handle send pwd reset: %w", err)
		}

		return nil
	}
}
