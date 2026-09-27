// Package events is the Redis list queue carrying click events from the API to the worker.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	queueKey = "spur:clicks"
	// maxQueued caps the backlog so a stopped worker can't exhaust Redis memory; the oldest events drop first.
	maxQueued = 100_000
)

type Click struct {
	LinkID    int64     `json:"link_id"`
	At        time.Time `json:"at"`
	Referrer  string    `json:"referrer"`
	UserAgent string    `json:"ua"`
	IPHash    string    `json:"ip_hash"`
}

func Publish(ctx context.Context, rdb *redis.Client, c Click) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	pipe := rdb.Pipeline()
	pipe.LPush(ctx, queueKey, b)
	pipe.LTrim(ctx, queueKey, 0, maxQueued-1)
	_, err = pipe.Exec(ctx)
	return err
}

// Requeue puts events back at the consumer end so they're retried first.
func Requeue(ctx context.Context, rdb *redis.Client, clicks []Click) error {
	vals := make([]any, 0, len(clicks))
	for i := len(clicks) - 1; i >= 0; i-- {
		b, err := json.Marshal(clicks[i])
		if err != nil {
			return err
		}
		vals = append(vals, b)
	}
	return rdb.RPush(ctx, queueKey, vals...).Err()
}

// Receive blocks up to wait for one event, then drains up to max-1 more without blocking.
// It returns nil, nil when the wait elapses with nothing queued.
func Receive(ctx context.Context, rdb *redis.Client, wait time.Duration, max int) ([]Click, error) {
	first, err := rdb.BRPop(ctx, wait, queueKey).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := []string{first[1]}
	if max > 1 {
		more, err := rdb.RPopCount(ctx, queueKey, max-1).Result()
		if err == nil {
			raw = append(raw, more...)
		}
	}

	clicks := make([]Click, 0, len(raw))
	for _, r := range raw {
		var c Click
		if json.Unmarshal([]byte(r), &c) == nil {
			clicks = append(clicks, c)
		}
	}
	return clicks, nil
}
