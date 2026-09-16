package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/gozsunday/gater/internal/cache"
	"github.com/gozsunday/gater/internal/config"
	"github.com/gozsunday/gater/internal/db"
	"github.com/gozsunday/gater/internal/mailer"
	"github.com/gozsunday/gater/internal/store"
	"github.com/gozsunday/gater/internal/validator"
	"github.com/gozsunday/gater/internal/worker"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := initLogger()

	cfg := loadConfig(logger)

	pool := initDBPool(ctx, cfg, logger)
	defer pool.Close()

	dbStore := store.New(pool)

	emailer := mailer.NewResendClient(cfg)

	validator := validator.New()

	redisClient := initRedisClient(ctx, cfg, logger)
	defer redisClient.Close()

	workerClient, workerServer, workerScheduler := startWorkers(
		cancel,
		redisClient,
		emailer,
		dbStore,
		logger,
	)
	// the worker infra close/shutdown must be deferred in the order:
	// client -> server -> scheduler since deferred code runs in a LIFO
	// (Last In First Out) order
	defer workerClient.Close()
	defer workerServer.Shutdown()
	defer workerScheduler.Shutdown()

	// init app
	app := &application{
		config:    cfg,
		store:     dbStore,
		mailer:    emailer,
		worker:    workerClient,
		validator: validator,
		logger:    logger,
	}

	if err := app.run(app.mount()); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

}

func initLogger() *slog.Logger {
	l := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(l)
	return l
}

func loadConfig(l *slog.Logger) *config.Config {
	cfg, err := config.Load()
	if err != nil {
		l.Error(err.Error())
		os.Exit(1)
	}
	return cfg
}

func initDBPool(ctx context.Context, cfg *config.Config, l *slog.Logger) *pgxpool.Pool {
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		l.Error("failed to create db pool", "error", err)
		os.Exit(1)
	}
	l.Info("database connection pool established")
	return pool
}

func initRedisClient(ctx context.Context, cfg *config.Config, l *slog.Logger) *redis.Client {
	rc, err := cache.NewRedisClient(ctx, cfg)
	if err != nil {
		l.Error("failed to create redis client", "error", err)
		os.Exit(1)
	}
	return rc
}

// startWorkers wires all asynq infrastructure: client (for Enqueue from HTTP
// handlers), server (consumes tasks), and scheduler (enqueues periodic jobs).
// it must be called after mailer, store, and redisClient are ready because
// NewServeMux captures them to run handlers
func startWorkers(
	cancel context.CancelFunc,
	rc *redis.Client,
	m mailer.Mailer,
	s store.Store,
	l *slog.Logger,
) (*asynq.Client, *asynq.Server, *asynq.Scheduler) {
	// must be init after mailer & dbStore and before worker server
	workerClient := worker.NewClient(rc)

	workerServer := worker.NewServer(rc)
	go func() {
		err := workerServer.Run(worker.NewServeMux(m, s, workerClient, l))
		if err != nil {
			l.Error("failed to run worker (asynq) server", "error", err)
			cancel()
		}
	}()

	workerScheduler := worker.NewScheduler(rc)

	endExpiredEventsEntryID, err := workerScheduler.Register(
		"@every 5m",
		asynq.NewTask(worker.TypeEndExpiredEvents, nil),
	)
	if err != nil {
		l.Error(
			"failed to run register periodic worker task",
			"error", err,
			"task", worker.TypeEndExpiredEvents,
		)
		os.Exit(1)
	}
	l.Info("registered a periodic worker entry", "entry_id", endExpiredEventsEntryID)

	expireWaitlistReservationID, err := workerScheduler.Register(
		"@every 5m",
		asynq.NewTask(worker.TypeExpireWaitlistReservations, nil),
	)
	if err != nil {
		l.Error(
			"failed to run register periodic worker task",
			"error", err,
			"task", worker.TypeExpireWaitlistReservations,
		)
		os.Exit(1)
	}
	l.Info("registered a periodic worker entry", "entry_id", expireWaitlistReservationID)

	// run scheduler
	go func() {
		if err := workerScheduler.Run(); err != nil {
			l.Error("failed to run worker (asynq) scheduler", "error", err)
			cancel()
		}
	}()

	return workerClient, workerServer, workerScheduler
}
