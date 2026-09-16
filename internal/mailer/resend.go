package mailer

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/gozsunday/gater/internal/config"
	"github.com/resend/resend-go/v3"
)

type resendClient struct {
	mailer      *resend.Client
	domain      string
	frontendURL string
}

func NewResendClient(cfg *config.Config) *resendClient {
	return &resendClient{
		mailer:      resend.NewClient(cfg.ResendAPIKey),
		domain:      cfg.ResendDomain,
		frontendURL: cfg.FrontendURL,
	}
}

func (r *resendClient) SendEmail(
	ctx context.Context,
	to []string,
	subject, html string,
) error {
	params := &resend.SendEmailRequest{
		To:      to,
		From:    getFrom(r.domain),
		Html:    html,
		Subject: subject,
	}

	_, err := r.mailer.Emails.SendWithContext(ctx, params)
	if err != nil {
		return fmt.Errorf("mailer: send email: %w", err)
	}

	return nil
}

func (r *resendClient) SendVerificationEmail(
	ctx context.Context,
	to []string,
	name, token string,
) error {
	tmpl, err := template.ParseFS(templates, "templates/verification.html")
	if err != nil {
		return fmt.Errorf("mailer: parse template: %w", err)
	}

	var body bytes.Buffer
	err = tmpl.Execute(&body, map[string]string{
		"Name":      name,
		"VerifyURL": r.frontendURL + "/verify?token=" + token,
	})
	if err != nil {
		return fmt.Errorf("mailer: execute template: %w", err)
	}

	return r.SendEmail(ctx, to, "Verify your email", body.String())
}

func (r *resendClient) SendPasswordResetEmail(
	ctx context.Context,
	to []string,
	name, token string,
) error {
	tmpl, err := template.ParseFS(templates, "templates/password-reset.html")
	if err != nil {
		return fmt.Errorf("mailer: parse template: %w", err)
	}

	var body bytes.Buffer
	err = tmpl.Execute(&body, map[string]string{
		"Name":     name,
		"ResetURL": r.frontendURL + "/password-reset?token=" + token,
	})
	if err != nil {
		return fmt.Errorf("mailer: execute template: %w", err)
	}

	return r.SendEmail(ctx, to, "Reset your password", body.String())
}

func (r *resendClient) SendWaitlistNotification(
	ctx context.Context,
	to []string,
	name, tierName, eventName string,
	expiresAt time.Time,
) error {
	tmpl, err := template.ParseFS(templates, "templates/waitlist-available.html")
	if err != nil {
		return fmt.Errorf("mailer: parse template: %w", err)
	}

	var body bytes.Buffer
	err = tmpl.Execute(&body, map[string]string{
		"Name":      name,
		"TierName":  tierName,
		"EventName": eventName,
		"ExpiresAt": expiresAt.UTC().Format("Mon, 02 Jan 2006 15:04 UTC"),
	})
	if err != nil {
		return fmt.Errorf("mailer: execute template: %w", err)
	}

	return r.SendEmail(ctx, to, "A ticket is now available — "+eventName, body.String())
}

func (r *resendClient) SendEventUpdatedNotification(
	ctx context.Context,
	to []string,
	name, eventName string,
	changedFields map[string]string,
	materialChangedAt time.Time,
) error {
	tmpl, err := template.ParseFS(templates, "templates/event-updated.html")
	if err != nil {
		return fmt.Errorf("mailer: parse template: %w", err)
	}

	var body bytes.Buffer
	// join changed fields into a readable list for the template
	var changedList strings.Builder
	for k, v := range changedFields {
		fmt.Fprintf(&changedList, "%s: %s\n", k, v)
	}
	// grace deadline is 72h from the stamp, capped at event start by the
	// cancellation policy, but we surface the raw deadline for clarity
	graceUntil := materialChangedAt.Add(72 * time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04 UTC")

	err = tmpl.Execute(&body, map[string]string{
		"Name":            name,
		"EventName":       eventName,
		"ChangedFields":   changedList.String(),
		"MaterialChanged": materialChangedAt.UTC().Format("Mon, 02 Jan 2006 15:04 UTC"),
		"GraceUntil":      graceUntil,
	})
	if err != nil {
		return fmt.Errorf("mailer: execute template: %w", err)
	}

	return r.SendEmail(ctx, to, "Event updated — "+eventName, body.String())
}

func (r *resendClient) SendEventCancelledNotification(
	ctx context.Context,
	to []string,
	name, eventName string,
	cancelledAt time.Time,
) error {
	tmpl, err := template.ParseFS(templates, "templates/event-cancelled.html")
	if err != nil {
		return fmt.Errorf("mailer: parse template: %w", err)
	}

	var body bytes.Buffer
	err = tmpl.Execute(&body, map[string]string{
		"Name":        name,
		"EventName":   eventName,
		"CancelledAt": cancelledAt.UTC().Format("Mon, 02 Jan 2006 15:04 UTC"),
	})
	if err != nil {
		return fmt.Errorf("mailer: execute template: %w", err)
	}

	return r.SendEmail(ctx, to, "Event cancelled — "+eventName, body.String())
}
