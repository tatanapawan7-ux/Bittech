package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestMemoryBurstThenRefill(t *testing.T) {
	lim := NewMemory(Config{Rate: 10, Burst: 3})
	base := time.Unix(1000, 0)
	lim.now = func() time.Time { return base }
	ctx := context.Background()

	// Burst of 3 is allowed immediately, the 4th is denied.
	for i := 0; i < 3; i++ {
		if ok, _ := lim.Allow(ctx, "k"); !ok {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if ok, _ := lim.Allow(ctx, "k"); ok {
		t.Fatal("4th request should be denied")
	}

	// After 0.1s at 10/s, exactly one token is back.
	base = base.Add(100 * time.Millisecond)
	if ok, _ := lim.Allow(ctx, "k"); !ok {
		t.Fatal("token should have refilled")
	}
	if ok, _ := lim.Allow(ctx, "k"); ok {
		t.Fatal("only one token should have refilled")
	}
}

func TestMemoryKeysAreIndependent(t *testing.T) {
	lim := NewMemory(Config{Rate: 1, Burst: 1})
	ctx := context.Background()
	if ok, _ := lim.Allow(ctx, "a"); !ok {
		t.Fatal("a should be allowed")
	}
	if ok, _ := lim.Allow(ctx, "b"); !ok {
		t.Fatal("b should be independent of a")
	}
	if ok, _ := lim.Allow(ctx, "a"); ok {
		t.Fatal("a should now be exhausted")
	}
}

func TestRedisLimiter(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	defer rdb.Close()

	lim := NewRedis(rdb, Config{Rate: 5, Burst: 2}, "test")
	fixed := time.Unix(2000, 0)
	lim.now = func() time.Time { return fixed }
	key := "user42"
	rdb.Del(ctx, "test:"+key)

	// Burst of 2 then denial, at a frozen clock so no refill happens.
	if ok, _ := lim.Allow(ctx, key); !ok {
		t.Fatal("1st allowed")
	}
	if ok, _ := lim.Allow(ctx, key); !ok {
		t.Fatal("2nd allowed")
	}
	if ok, _ := lim.Allow(ctx, key); ok {
		t.Fatal("3rd should be denied")
	}

	// Advance 1s -> 5 tokens/s refills the bucket (capped at burst 2).
	fixed = fixed.Add(time.Second)
	if ok, _ := lim.Allow(ctx, key); !ok {
		t.Fatal("should refill after 1s")
	}
}
