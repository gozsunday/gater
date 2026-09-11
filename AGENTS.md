# Gater

Event ticketing API. Go 1.26, PostgreSQL, Redis.

## Quick start

```sh
just dev       # docker compose up -d + air (hot reload)
just d         # alias for dev
just migrate   # runs goose migrations (go run cmd/migrate/main.go)
just db-up     # docker compose up -d (PG:5435, Redis:6380)
just db-down   # docker compose down
just db-delete # docker compose down -v
go build -o bin/server ./cmd/server
```

No tests, no linter, no formatter config.

## Architecture

`cmd/server/` — `package main`, HTTP handlers, chi routing, middleware.  
`internal/` — `config/` (godotenv), `db/` (pgxpool), `store/` (raw SQL via pgx, 5s timeout on multi-statement transactions only — single queries inherit the caller's ctx; `users`, `sessions`, `verifications`, `oauth`, `events`, `tiers`, `purchases`, `waitlist`, `tickets`), `auth/` (argon2id, SHA-256 tokens), `jsonutil/`, `validator/` (go-playground), `mailer/` (Resend), `qr/` (HMAC-SHA256 ticket tokens).  
`cmd/migrate/` — goose runner with embedded SQL.  
`internal/cache/redis.go` is the Redis factory, wired in `main.go` → shared `*redis.Client` reused by Asynq client/server/scheduler (`internal/worker/client.go`, `server.go`); `internal/worker/` holds email + waitlist + periodic handlers.

Handlers manually wired into `application` struct in `main.go` — no DI framework.

## Key conventions

- **Auth:** `authenticate` is the single auth core (Bearer header first, `gater_auth_session` cookie fallback). Wrappers: `requireAuth` (rejects with 401), `maybeAuth` (never rejects — runs handlers as guest when unauthenticated). Token = 32-byte random → hex → SHA-256 → store hash. Session create retries up to 3× on hash collision.
- **Middleware order:** `requireAuth` → `requireOrganizer` (role check) → `requireEventOrganizer` (ownership check, loads event into `eventCtx`).
- **Public-event visibility:** draft/cancelled events return 404 to non-organizers (no existence leak). `publicEvent` helper enforces this on public routes.
- **Event updates:** draft events are fully editable. Published/sold_out: material fields (dates, location, cancellation policy) need `confirm_material_change: true`, capacity can't drop below tickets sold, and a confirmed material change stamps `material_changed_at` — opening a 72h purchase-cancellation grace window (preserved via COALESCE on cosmetic updates); cancelled/ended events are frozen (409). Name/description/max_tickets editable anytime.
- **Event cancellation:** flips every confirmed purchase (and its tickets) to `cancelled` and hard-deletes waitlist entries for the event (synchronous bulk via `Purchases.CancelByEvent` / `Waitlist.DeleteByEvent`; comment notes job/batching for 10k+). Buyer snapshot is taken before the flip so `NotifyBuyersCancelled` (fan-out per-buyer, log-and-continue) never misses recipients due to the race. Refunds stay TODO (blocked on payments).
- **Cookie** `gater_auth_session` set on login (HttpOnly, Lax, 30d, Secure only in production, `SameSite=Lax`). CORS `AllowCredentials: true` lets browsers send it cross-origin.
- **JSON response:** Success `{"data": ...}` via `WriteData`, errors `{"errors": [...]}` via `WriteError` (single msg) / `WriteErrors` (validation). Exceptions: health check uses bare `Write` → `{"status":"OK"}`; check-in uses always-200 Option A (`{"valid": true/false}`) — domain outcomes are expected scan results, not HTTP errors.
- **Password** `json:"-"` — never serialized to JSON. `internal/store/` uses raw SQL; `PurchasesStore.Create`, `PurchasesStore.Cancel`, and `TicketsStore.CheckIn` are the only transactions so far (`pool.Begin` + deferred rollback inside one store method — handlers never see `pgx.Tx`).
- **Check-in:** event-scoped `POST /api/events/{id}/checkin` (inside `requireEventOrganizer` — wrong-event detection uses `eventCtx`; deviation from PLAN's flat `/api/checkin`). Single tx with `SELECT FOR UPDATE OF t` (scoped to the ticket row only). Responses are always-200 `{"data": {"valid": ...}}` (Option A) — domain outcomes (`invalid token`, `already checked in`, `ticket cancelled`, `wrong event`) return `{"valid": false, "reason": ...}`; only malformed JSON hits the standard `{"errors": [...]}` envelope.
- **Purchases:** buying requires event `published` + tier `available` (authoritative inside the tx); `total` is computed from the locked tier price, never trusted from the client. Purchase history is offset-paginated (`page`/`limit`/`total`) — an archive reads better in pages than the cursor-style events feed. Cancellation policy: rejects once the event started or ended, honours `cancellation_hours_before` plus the 72h material-change grace window, allows cancelling against an already-cancelled event, restores inventory and reopens sold_out events.
- **Waitlist:** sold-out tiers only (`409` otherwise); one entry per user per tier via `UNIQUE(user_id, tier_id)` — duplicates and expired entries 409 until Phase 12 adds conditional rejoin; leaving is a hard pair-scoped DELETE (no event/tier fetches needed — mismatches all collapse into one 404); organizer view is FIFO with buyer identity. Promotion-on-cancellation is enqueued in `cancelPurchase` handler → `worker.NotifyNextWaiting` when `wasSoldOut` (gated before restore).
- **Background email** via Asynq single default queue: `worker.TypeSendVerificationEmail` / `TypeSendPasswordResetEmail` and `TypeNotifyBuyersUpdated` / `TypeNotifyBuyersCancelled` + `worker.TypeNotifyWaitlistEntry` (all fan-out per-buyer via lookup pattern, log-and-continue, `SkipRetry` on empty queue); `mailer.SendWaitlistNotification` + `SendEvent*Notification` (+ `templates/event-*.html`); periodic `HandleEndExpiredEvents` / `HandleExpireWaitlistReservations` (`@every 5m` via Scheduler).
- **Tier payloads:** `price`/`quantity` are pointers on create (omitted ≠ 0); update payloads are pointer-based so omitted fields stay unchanged.
- **Tier updates:** `name`/`price` editable anytime (purchase totals are stored per purchase); `quantity` must stay `>= sold` (422) with `remaining` recomputed; capacity check nets out the tier's old quantity; tier `sold_out → available` and event `sold_out → published` flips on quantity increase; delete is draft-only; cancelled/ended events freeze all tier edits (409).

## Routes (`/api`)

```
GET  /api/health
POST /api/auth/register, login, verify-email, resend-verification, forgot-password, reset-password
GET  /api/auth/google, google/callback
POST /api/auth/logout, become-organizer  (protected)
GET  /api/auth/me                        (protected)

GET    /api/events                      (public, cursor-paginated published only)
GET    /api/events/{id}                 (public; drafts private via publicEvent)
POST   /api/events                      (organizer)
PATCH  /api/events/{id}                 (organizer, ownership)
DELETE /api/events/{id}                 (organizer, draft only)
POST   /api/events/{id}/publish         (organizer, draft only)
POST   /api/events/{id}/cancel          (organizer, draft/published/sold_out)
GET    /api/events/{id}/tiers           (public; drafts private)
POST   /api/events/{id}/tiers           (organizer, draft only, capacity-checked)
PATCH  /api/events/{id}/tiers/{tierId}  (organizer, ownership; quantity >= sold, sold_out flips)
DELETE /api/events/{id}/tiers/{tierId}  (organizer, draft only)
GET    /api/purchases                   (protected; offset-paginated history: page/limit/total)
POST   /api/purchases                   (protected; tx: tier lock → checks → decrement → tickets)
GET    /api/purchases/{id}              (protected; owner-scoped 404, full event/tier/tickets nested)
POST   /api/purchases/{id}/cancel       (protected; policy tx: gates → flip → restore → reopen)
POST   /api/events/{id}/tiers/{tierId}/waitlist  (protected attendee route; sold-out tiers only)
DELETE /api/events/{id}/tiers/{tierId}/waitlist  (protected; hard delete, own row only)
GET    /api/events/{id}/waitlist                 (organizer; FIFO with buyer identity)
POST   /api/events/{id}/checkin                  (organizer; event-scoped, always-200 valid/reason)
```

## Implemented vs stubs

| File                                                                                  | Status      |
| ------------------------------------------------------------------------------------- | ----------- |
| `auth.go`, `users.go`, `health.go`, `middleware.go`, `events.go`, `tiers.go`, `purchases.go`, `waitlist.go`, `check-in.go` | Implemented |
| `analytics.go`                                                                          | Empty stub  |

## Requests

`requests/` directory contains a [Bruno](https://docs.usebruno.com) API collection (`opencollection.yml`) with request examples for auth endpoints.

## Known quirks

- None currently. Redis is now wired via Asynq client/server/scheduler using `internal/cache/redis.go`.
