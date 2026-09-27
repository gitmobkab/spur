// Command worker drains click events from Redis into Postgres.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gitmobkab/spur/internal/config"
	"github.com/gitmobkab/spur/internal/events"
	"github.com/gitmobkab/spur/internal/store"
	"github.com/gitmobkab/spur/internal/useragent"
)

const batchSize = 200

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if err := config.Require("DATABASE_URL", "REDIS_URL"); err != nil {
		return err
	}
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	log.Info("worker started")
	backoff := time.Second
	for ctx.Err() == nil {
		batch, err := events.Receive(ctx, rdb, 5*time.Second, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Error("receive failed", "err", err)
			sleep(ctx, backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		if len(batch) == 0 {
			continue
		}

		clicks := make([]store.Click, len(batch))
		for i, e := range batch {
			clicks[i] = store.Click{
				LinkID:    e.LinkID,
				ClickedAt: e.At,
				Referrer:  truncate(e.Referrer, 255),
				Device:    useragent.Classify(e.UserAgent),
				IPHash:    e.IPHash,
			}
		}

		// Use a fresh context so a batch already popped still lands during shutdown.
		insertCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = st.InsertClicks(insertCtx, clicks)
		if err != nil {
			log.Error("insert failed, requeueing", "count", len(batch), "err", err)
			if rqErr := events.Requeue(insertCtx, rdb, batch); rqErr != nil {
				log.Error("requeue failed, events lost", "count", len(batch), "err", rqErr)
			}
			cancel()
			sleep(ctx, backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		cancel()
		backoff = time.Second
		log.Info("recorded clicks", "count", len(clicks))
	}

	log.Info("worker stopped")
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
