alias d := dev

# Start the development environment
dev: db-up
    air

# Build the server binary
build:
    go build -o bin/server ./cmd/server

# Run database migrations
migrate:
    go run cmd/migrate/main.go

# Bring up the DB and Redis containers
db-up:
    docker compose up -d

# Bring down the DB and Redis containers
db-down:
    docker compose down

# Bring down the DB and Redis containers and delete volumes
db-delete:
    docker compose down -v

# Run tests with disabled cache
test:
  go test -count=1 -p 1 ./...
