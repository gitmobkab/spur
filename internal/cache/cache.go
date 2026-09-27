// Package cache holds spur's Redis-backed link cache and rate limiter.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry is a cached slug lookup. ID 0 marks a cached miss.
type Entry struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func linkKey(slug string) string { return "spur:link:" + slug }

func GetLink(ctx context.Context, rdb *redis.Client, slug string) (Entry, bool, error) {
	raw, err := rdb.Get(ctx, linkKey(slug)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

func SetLink(ctx context.Context, rdb *redis.Client, slug string, e Entry, ttl time.Duration) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return rdb.Set(ctx, linkKey(slug), b, ttl).Err()
}

func DeleteLink(ctx context.Context, rdb *redis.Client, slug string) error {
	return rdb.Del(ctx, linkKey(slug)).Err()
}

// Allow is a fixed-window rate limiter: at most limit hits per window for key.
func Allow(ctx context.Context, rdb *redis.Client, key string, limit int, window time.Duration) (bool, error) {
	bucket := fmt.Sprintf("spur:rl:%s:%d", key, time.Now().UnixNano()/int64(window))
	pipe := rdb.TxPipeline()
	incr := pipe.Incr(ctx, bucket)
	pipe.Expire(ctx, bucket, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return incr.Val() <= int64(limit), nil
}
