# Gater

Gater is an event-ticketing backend API built with Go, PostgreSQL, and Redis. It supports event management, ticket inventory, purchases, waitlists, QR-based check-in, background email delivery, and scheduled lifecycle jobs.

> This project is under active development and is not ready for production use.

## Overview

Gater models the full lifecycle of a ticketed event:

- Organizers create and manage events and ticket tiers.
- Attendees purchase tickets, join waitlists, cancel purchases, and check in with QR tokens.
- Background workers handle email delivery, waitlist promotion, event expiry, and buyer notifications.

The implementation emphasizes transactional inventory updates, explicit event-state transitions, organizer ownership checks, and auditable purchase records.

## Key features

- Email/password authentication with verification and password reset.
- Google OAuth account linking and session authentication.
- Organizer roles and event-ownership authorization.
- Draft, published, sold-out, cancelled, and ended event states.
- Capacity-aware ticket tiers with remaining-inventory tracking.
- Transactional ticket purchases with server-calculated totals.
- Purchase cancellation rules, including a material-change grace window.
- Sold-out tier waitlists with FIFO promotion and expiry handling.
- HMAC-signed QR tickets and event-scoped check-in.
- Asynq background jobs for email, waitlist promotion, expiry, and buyer notifications.

## Tech stack

| Concern | Choice |
| --- | --- |
| HTTP API | Go, Chi router |
| Database | PostgreSQL, pgx, Goose migrations |
| Cache and queue | Redis, Asynq |
| Authentication | Argon2id, SHA-256 session tokens, Google OAuth 2.0 |
| Email | Resend |
| QR tokens | HMAC-SHA256 |
| Local services | Docker Compose, Air live reload |

## Architecture

```text
cmd/server/
  HTTP handlers, Chi routing, middleware, application wiring

cmd/migrate/
  Goose migration runner with embedded SQL migrations

internal/
  config/      Environment configuration
  db/          PostgreSQL connection pool
  store/       Raw SQL data-access layer
  auth/        Password hashing and token helpers
  qr/          Ticket token signing and verification
  mailer/      Resend client and email templates
  validator/   Request validation
  cache/       Shared Redis client factory
  worker/      Asynq client, server, scheduler, and handlers
```

There is no dependency-injection framework. The server constructs an `application` value containing configuration, storage, mailer, validator, logger, and background-job client.

Authentication supports bearer tokens with an HTTP-only session-cookie fallback. Organizer-only routes add role and event-ownership checks.

Successful responses use:

```json
{"data": {}}
```

Errors use:

```json
{"errors": ["message"]}
```

Ticket check-in is an exception: scanner results always return HTTP 200 with:

```json
{"valid": true}
```

or:

```json
{"valid": false, "reason": "already checked in"}
```

## API overview

### Authentication

```text
POST /api/auth/register
POST /api/auth/login
POST /api/auth/verify-email
POST /api/auth/resend-verification
POST /api/auth/forgot-password
POST /api/auth/reset-password
GET  /api/auth/google
GET  /api/auth/google/callback
POST /api/auth/logout
POST /api/auth/become-organizer
GET  /api/auth/me
```

### Events and tiers

```text
GET    /api/events
GET    /api/events/{id}
POST   /api/events
PATCH  /api/events/{id}
DELETE /api/events/{id}
POST   /api/events/{id}/publish
POST   /api/events/{id}/cancel
GET    /api/events/{id}/tiers
POST   /api/events/{id}/tiers
PATCH  /api/events/{id}/tiers/{tierId}
DELETE /api/events/{id}/tiers/{tierId}
```

### Purchases, waitlist, and check-in

```text
GET    /api/purchases
POST   /api/purchases
GET    /api/purchases/{id}
POST   /api/purchases/{id}/cancel
POST   /api/events/{id}/tiers/{tierId}/waitlist
DELETE /api/events/{id}/tiers/{tierId}/waitlist
GET    /api/events/{id}/waitlist
POST   /api/events/{id}/checkin
```

Organizer analytics remain planned and are not yet implemented.

## Data model and lifecycle

Core entities include users, sessions, events, ticket tiers, purchases, tickets, waitlist entries, verifications, and OAuth accounts.

Supported event transitions include:

```text
draft      → published
draft      → cancelled
published  → sold_out
sold_out   → published
published  → cancelled
sold_out   → cancelled
published  → ended
sold_out   → ended
```

Important rules:

- Draft and cancelled events are hidden from non-organizers.
- Published-event material changes require explicit confirmation.
- Tier inventory cannot fall below tickets already sold.
- Event capacity cannot fall below confirmed ticket sales.
- Cancelling an event flips its confirmed purchases and tickets to cancelled and clears its waitlist.

## Concurrency and background processing

Purchases lock the relevant tier row inside a database transaction, recalculate totals from stored prices, decrement inventory, create purchase and ticket rows, and update sold-out state without trusting client-supplied totals.

Check-in locks the individual ticket row before marking it used, preventing duplicate scans from concurrent requests.

Asynq handles:

- Verification and password-reset email.
- Waitlist promotion and reservation expiry.
- Event expiry.
- Buyer notifications for material event updates and cancellations.

Recurring jobs run every five minutes.

## Getting started

### Prerequisites

- Go 1.26
- Docker and Docker Compose
- `just`
- Air, for live reload during development

### Run locally

```sh
cp .env.example .env
just db-up
just migrate
just dev
```

The API is available at:

```text
http://localhost:8080
```

Health check:

```sh
curl http://localhost:8080/api/health
```

Build the server binary:

```sh
go build -o bin/server ./cmd/server
```

Other useful commands:

```sh
just db-down
just db-delete
just migrate
just build
```

## Configuration

| Variable | Purpose | Default/example |
| --- | --- | --- |
| `PORT` | API listen port | `8080` |
| `ENV` | Runtime environment | `development` |
| `FRONTEND_URL` | Links used in emails | `http://localhost:3000` |
| `DATABASE_URL` | PostgreSQL connection | `postgresql://user:secret@localhost:5435/gater?sslmode=disable` |
| `REDIS_URL` | Redis/Asynq connection | `redis://localhost:6380` |
| `CORS_ALLOWED_ORIGIN` | Browser origin allowed to call the API | `http://localhost:3000` |
| `RESEND_API_KEY` | Email provider credential | unset |
| `RESEND_DOMAIN` | Sender domain | unset |
| `GOOGLE_CLIENT_ID` | Google OAuth client | unset |
| `GOOGLE_CLIENT_SECRET` | Google OAuth secret | unset |
| `GOOGLE_REDIRECT_URI` | OAuth callback | `http://localhost:8080/api/auth/google/callback` |
| `TICKET_SECRET` | QR-token signing secret | generated with `openssl rand -hex 32` |

Local Docker services expose PostgreSQL on `5435` and Redis on `6380`.

## Development

The project uses Goose migrations embedded in `cmd/migrate`, raw SQL through `pgx`, and manually wired application dependencies.

There is currently no automated test suite, linter configuration, or formatter configuration.

Useful checks:

```sh
go vet ./...
gofmt -l .
```

API request examples for authentication are available in the Bruno collection under `requests/`.

## Roadmap

Completed:

- Authentication, sessions, roles, and OAuth.
- Events, tiers, purchases, tickets, waitlist entry management, and check-in.
- Background email delivery and scheduled expiry/promotion jobs.
- Buyer notifications for material changes and cancellations.

Remaining:

- Organizer analytics endpoints.
- API documentation.
- Production Docker/deployment configuration.
- Optional custom ticket-tier ordering.
