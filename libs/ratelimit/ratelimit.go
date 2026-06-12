// Package ratelimit provides a token-bucket rate limiter for the API edge.
//
// Two backends implement the same Limiter interface:
//   - RedisLimiter: a Lua-scripted token bucket, atomic and shared across all
//     API instances (the production choice — limits must be global, not per-pod).
//   - MemoryLimiter: an in-process bucket for single-node dev and tests.
//
// A token bucket (rather than a fixed window) is used because it allows short
// bursts up to the bucket size while bounding the sustained rate — the standard
// shape for exchange API limits.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter decides whether an action keyed by `key` may proceed now.
type Limiter interface {
	// Allow reports whether a request for key is permitted, consuming one token
	// if so. It never blocks.
	Allow(ctx context.Context, key string) (bool, error)
}

// Config defines a bucket: Rate tokens are added per second up to Burst tokens.
type Config struct {
	Rate  float64 // sustained tokens per second
	Burst float64 // maximum bucket size
}

// MemoryLimiter is an in-process token-bucket limiter, safe for concurrent use.
type MemoryLimiter struct {
	cfg     Config
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewMemory creates an in-memory limiter.
func NewMemory(cfg Config) *MemoryLimiter {
	return &MemoryLimiter{cfg: cfg, buckets: make(map[string]*bucket), now: time.Now}
}

// Allow implements Limiter.
func (m *MemoryLimiter) Allow(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	b, ok := m.buckets[key]
	if !ok {
		b = &bucket{tokens: m.cfg.Burst, last: now}
		m.buckets[key] = b
	}
	// Refill proportional to elapsed time, capped at the burst size.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = min(m.cfg.Burst, b.tokens+elapsed*m.cfg.Rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, nil
	}
	return false, nil
}

// tokenBucketScript atomically refills and consumes one token. Keeping the
// whole computation server-side makes it safe under concurrent clients.
// KEYS[1] = bucket key; ARGV = rate, burst, now_seconds, ttl_seconds.
var tokenBucketScript = redis.NewScript(`
local key   = KEYS[1]
local rate  = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now   = tonumber(ARGV[3])
local ttl   = tonumber(ARGV[4])

local data    = redis.call('HMGET', key, 'tokens', 'ts')
local tokens  = tonumber(data[1])
local ts      = tonumber(data[2])
if tokens == nil then tokens = burst; ts = now end

local elapsed = math.max(0, now - ts)
tokens = math.min(burst, tokens + elapsed * rate)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call('HMSET', key, 'tokens', tokens, 'ts', now)
redis.call('EXPIRE', key, ttl)
return allowed
`)

// RedisLimiter is a distributed token-bucket limiter backed by Redis.
type RedisLimiter struct {
	rdb    *redis.Client
	cfg    Config
	prefix string
	now    func() time.Time
}

// NewRedis creates a Redis-backed limiter. Keys are namespaced by prefix.
func NewRedis(rdb *redis.Client, cfg Config, prefix string) *RedisLimiter {
	return &RedisLimiter{rdb: rdb, cfg: cfg, prefix: prefix, now: time.Now}
}

// Allow implements Limiter via the atomic Lua script.
func (r *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	// TTL covers the time to refill a full bucket plus slack, so idle keys expire.
	ttl := int64(r.cfg.Burst/r.cfg.Rate) + 60
	res, err := tokenBucketScript.Run(ctx, r.rdb,
		[]string{r.prefix + ":" + key},
		r.cfg.Rate, r.cfg.Burst, r.now().UnixNano()/1e9, ttl).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}
