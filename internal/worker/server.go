package worker

import (
	"log/slog"
	"time"

	"github.com/gozsunday/gater/internal/mailer"
	"github.com/gozsunday/gater/internal/store"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

func NewServer(rc *redis.Client) *asynq.Server {
	s := asynq.NewServerFromRedisClient(
		rc,
		asynq.Config{Concurrency: 10},
	)
	return s
}

func NewServeMux(
	m mailer.Mailer,
	s store.Store,
	c *asynq.Client,
	l *slog.Logger,
) *asynq.ServeMux {
	mux := asynq.NewServeMux()

	// auth emails
	mux.HandleFunc(TypeSendVerificationEmail, HandleSendVerificationTask(m))
	mux.HandleFunc(TypeSendPasswordResetEmail, HandleSendPwdResetTask(m))

	// periodic
	mux.HandleFunc(TypeEndExpiredEvents, HandleEndExpiredEvents(s, l))
	mux.HandleFunc(TypeExpireWaitlistReservations, HandleExpireWaitlistReservations(s, c, l))

	// waitlist emails
	mux.HandleFunc(TypeNotifyWaitlistEntry, HandleNotifyNextWaiting(s, m))

	// buyer notifications
	mux.HandleFunc(TypeNotifyBuyersUpdated, HandleNotifyBuyersUpdated(s, m, l))
	mux.HandleFunc(TypeNotifyBuyersCancelled, HandleNotifyBuyersCancelled(m, l))

	return mux
}

func NewScheduler(rc *redis.Client) *asynq.Scheduler {
	return asynq.NewSchedulerFromRedisClient(rc, &asynq.SchedulerOpts{Location: time.UTC})
}
