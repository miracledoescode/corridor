// Package cache mirrors live top-of-book to Redis.
//
// WHY best-effort only: Postgres is the moat; Redis is a convenience for
// the future spread engine. A Redis outage must cost us nothing but a log
// line — never a quote.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const ttl = 60 * time.Second

// Entry is the live top-of-book for one outcome. Prices stay strings —
// the never-float rule applies to the cache too.
type Entry struct {
	Bid  string    `json:"bid,omitempty"`
	Ask  string    `json:"ask,omitempty"`
	Last string    `json:"last,omitempty"`
	Time time.Time `json:"time"`
}

type TopBook struct {
	rdb *redis.Client
	log *slog.Logger
}

func New(redisURL string, log *slog.Logger) (*TopBook, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &TopBook{rdb: redis.NewClient(opt), log: log}, nil
}

// Set writes one entry with a TTL. Failures are logged, never returned:
// callers on the ingest path must not branch on cache health.
func (t *TopBook) Set(ctx context.Context, venue, marketID, outcomeID string, e Entry) {
	b, err := json.Marshal(e)
	if err != nil {
		t.log.Warn("topbook marshal failed", "err", err)
		return
	}
	key := fmt.Sprintf("tob:%s:%s:%s", venue, marketID, outcomeID)
	if err := t.rdb.Set(ctx, key, b, ttl).Err(); err != nil {
		t.log.Warn("topbook set failed", "key", key, "err", err)
	}
}

func (t *TopBook) Close() error { return t.rdb.Close() }
