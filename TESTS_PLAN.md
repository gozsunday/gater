# Test Plan

How Gater's test suite is built, in what order, and why. Written to survive a
machine wipe: everything needed to resume is here, not in anyone's head.

## Goals

1. **Cover the critical paths.** The logic worth protecting is the logic that is
   hard to reason about: transactional ticket inventory, cancellation windows,
   check-in idempotency, and waitlist FIFO ordering.
2. **Learn Go testing by writing Go tests.** Tests are written by hand, one layer
   at a time, with review. Coverage is the goal; volume is not.

Out of scope: 100% coverage, benchmarks, fuzzing, load testing, golden files.

## Ground rules

These apply to every test written in this repo.

- **stdlib `testing` only.** No testify, no assertion libraries. Table-driven
  tests with `t.Run` are the idiom.
- **External test packages.** `package store_test`, not `package store`. Tests
  only touch exported API, which is the same surface the handlers use.
- **Assert sentinels with `errors.Is`.** `internal/store/store.go` exports
  `ErrConflict`, `ErrNotFound`, `ErrAlreadyCheckedIn`, `ErrOutsideCancellationWindow`
  and others. Never compare error strings.
- **One behavior per test.** If the test name needs "and", split it.
- **Unique fixture values per test.** Derive emails and names from UUIDs so a
  leaked row cannot make an unrelated test fail.
- **No `t.Parallel` for database tests.** Every package shares one `gater_test`
  database. Serial execution is what makes truncation a valid cleanup strategy.
- **Time-sensitive fixtures are relative to `time.Now()`.** No clock injection;
  the store reads the database clock, so set timestamps around "now" instead.
- **`t.Cleanup` for teardown, registered after setup.** Cleanup runs
  last-in-first-out, so truncation runs before the pool closes.

## Infrastructure

`internal/testdb/testdb.go`, two exported functions:

```go
func Open(t *testing.T) *pgxpool.Pool   // create + migrate gater_test, return pool
func Truncate(t *testing.T, pool *pgxpool.Pool) // clear data, keep schema
```

`Open` reads `TEST_DATABASE_URL`, defaulting to
`postgresql://user:secret@localhost:5435/gater_test?sslmode=disable`. It
connects to the `postgres` maintenance database to issue
`CREATE DATABASE "gater_test"` (ignoring Postgres error `42P04`, which means it
already exists), runs the same Goose migrations `cmd/migrate` runs, then returns
a `pgxpool.Pool` with `pool.Close` registered as cleanup.

**Why a separate database.** Tests truncate tables and create fixtures. Running
them against the development `gater` database would destroy local data and let a
dirty starting state produce confusing failures.

**Why the migrations are shared, not re-read from disk.** `cmd/migrate` is
`package main` and cannot be imported, so `cmd/migrate/migrations/embed.go`
holds `//go:embed *.sql` in an importable package. Both `cmd/migrate` and
`internal/testdb` consume that `migrations.FS`. An earlier version resolved the
migrations directory by walking up from `runtime.Caller(0)` to `go.mod`, which
broke under `-trimpath` because the compiler then reports module-relative paths.

Note the directory argument is `"."`, not `"migrations"`: `//go:embed *.sql`
strips the directory prefix, so the `.sql` files sit at the root of the
`embed.FS`. Goose stats the directory before globbing, so the wrong value fails
with `migrations directory does not exist`.

Run everything with:

```sh
just test    # go test -count=1 -p 1 ./...
```

`-count=1` defeats the result cache, so tests actually re-run after edits.
`-p 1` stops packages executing concurrently against the single test database.

## Layers

Ordered by teaching value, not by coverage. Do not skip ahead.

### Layer 1 — pure unit tests

No database, no HTTP, no Redis. These teach Go test mechanics with nothing else
in the way.

| Package          | What to cover                                                        |
| ---------------- | -------------------------------------------------------------------- |
| `internal/qr`    | token roundtrip, wrong secret, tampered payload, non-base64, bad shape |
| `internal/validator` | `ValidateStruct` and `ValidateVar` against a sample request struct |
| `internal/jsonutil`  | `Read`, `Write`, `WriteData`, `WriteError`, `WriteErrors` envelopes |
| `internal/cursor` | `Encode` / `Decode` roundtrip, malformed input, boundary values     |
| `internal/auth`  | `HashPassword` / `VerifyPassword`, `GenerateToken` / `HashToken` / `CompareToken`, `GenerateRandomOAuthState` |

### Layer 2 — store tests on the real database

Where the real value is. `store.New(pool)` returns a `Store` with `Users`,
`Sessions`, `Verifications`, `OAuthAccounts`, `Events`, `Tiers`, `Purchases`,
`Waitlist`, `Tickets`.

Progression, easiest first:

1. **Users** — create then fetch by email, field comparison; duplicate email
   returns `ErrConflict`.
2. **Sessions** — create, resolve a valid token, reject an unknown one.
3. **Events and tiers** — draft-to-published transitions, capacity checks,
   tier quantity floor (`ErrInsufficientRemaining` style rules).
4. **Purchases** — the crown jewel: N goroutines racing for the last ticket,
   asserting exactly one succeeds. This is the test that proves the
   `SELECT ... FOR UPDATE` on the tier row actually prevents overselling. Uses
   `sync.WaitGroup`; no `t.Parallel`.
5. **Purchase cancellation** — a table of windows: outside
   `cancellation_hours_before`, inside it, inside the 72h `material_changed_at`
   grace window, after event start, against an already-cancelled event.
   Cancellation also restores inventory and reopens a sold-out event; assert all
   three.
6. **Check-in** — valid scan succeeds; a second scan returns
   `ErrAlreadyCheckedIn`; a cancelled ticket returns `ErrTicketCancelled`; a
   ticket from another event returns `ErrWrongEvent`.
7. **Waitlist** — join requires a sold-out tier, one entry per user per tier,
   organizer view is FIFO by creation, and promotion fires after a cancellation.

### Layer 3 — HTTP handler tests

`net/http/httptest`, no server boot. `application{store: store.New(pool), ...}`
is a plain struct, so tests build it directly and call handlers.

Assert status codes and response envelopes, not store internals — the store is
already covered by Layer 2.

- `POST /api/auth/register` → 201 with `{"data": ...}`, duplicate → 409 with
  `{"errors": [...]}`.
- `POST /api/purchases` happy path and each rejection reason.
- `POST /api/events/{id}/checkin` always-200 `{"valid": true}` versus
  `{"valid": false, "reason": "..."}`, and the malformed-JSON case falling back
  to the standard error envelope.
- Middleware: 401 without a token, 403 for a non-organizer, 404 for
  another organizer's event.

### Layer 4 — worker handler tests

`internal/worker` handlers are ordinary functions taking `*asynq.Task`. Build
the task with `asynq.NewTask(worker.TypeSendVerificationEmail, payload)` and
call the handler directly. No Redis required at this level.

The `mailer.Mailer` interface is the seam: a ~10-line fake records what would
have been sent. Assert fan-out behavior — one notification per buyer,
log-and-continue when a single recipient fails.

## After a fresh reinstall

```sh
git clone <repo> && cd gater
just db-up                # Postgres on 5435, Redis on 6380
just migrate              # verifies the shared embed still resolves
go test ./internal/qr/... # first test to run, no database needed
just test                 # everything
```

If `just migrate` reports `migrations directory does not exist`, the
`goose.Up` directory argument regressed to `"migrations"` instead of `"."`.

`Open` creates and migrates `gater_test` on its own, so the test suite needs
nothing beyond a running Postgres. Redis is not required for Layers 1 through 3.

## Maintenance notes

- **A new table means a new line in `Truncate`.** The table list is explicit and
  deliberate. If a migration adds a table, add it to the `TRUNCATE` statement in
  `internal/testdb/testdb.go`, otherwise rows survive cleanup and tests fail in
  ways that look like store bugs.
- **Keep `Truncate`'s table list in sync with** `cmd/migrate/migrations/*.sql`.
  Current domain tables: `users`, `sessions`, `verifications`, `oauth_accounts`,
  `events`, `ticket_tiers`, `purchases`, `tickets`, `waitlist_entries`.
  `goose_db_version` is intentionally preserved so migration history accumulates
  and tests exercise the real migration path.
- **Run store tests serially.** A stray `t.Parallel` in a database test will
  produce truncation races that look like application bugs.
- **No store mocks.** Interfaces were deliberately collapsed to concrete types,
  so store tests run against real Postgres instead of fakes. Only `mailer.Mailer`
  remains an interface, and only because it crosses a network boundary.

## Progress

- [x] `internal/testdb` helper, `just test` target, shared migration embed
- [ ] `internal/qr`
- [ ] `internal/validator`
- [ ] `internal/jsonutil`
- [ ] `internal/cursor`
- [ ] `internal/auth`
- [ ] `internal/store` — users and sessions
- [ ] `internal/store` — events and tiers
- [ ] `internal/store` — purchases, including the concurrent oversell test
- [ ] `internal/store` — cancellation windows
- [ ] `internal/store` — check-in
- [ ] `internal/store` — waitlist
- [ ] `cmd/server` — HTTP handler tests
- [ ] `internal/worker` — handler tests with a fake mailer
