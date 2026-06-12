package ledger

// Integration tests against a real PostgreSQL. They self-provision a throwaway
// database (the dev user has CREATEDB) and apply the migrations from infra/db,
// so the exact production schema is what gets exercised. Skipped when Postgres
// is unreachable; set TEST_PG_URL to point somewhere else.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

func testService(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	base := os.Getenv("TEST_PG_URL")
	if base == "" {
		base = "postgres://bittech:bittech@127.0.0.1:5432/bittech"
	}
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	dbname := fmt.Sprintf("ledger_test_%d", os.Getpid())
	admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbname)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbname); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	admin.Close()

	pool, err := pgxpool.New(ctx, base[:len(base)-len("/bittech")]+"/"+dbname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Apply the real migrations in order.
	files, err := filepath.Glob("../../../infra/db/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	// Test fixtures: two assets and two users (ledger refs users/assets).
	mustExec(t, pool, `INSERT INTO assets (symbol, name, scale) VALUES ('BTC','Bitcoin',8), ('USDT','Tether',6) ON CONFLICT (symbol) DO NOTHING`)
	mustExec(t, pool, `INSERT INTO users (email) VALUES ('buyer@t.co'), ('seller@t.co')`)
	return New(pool)
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func mustBalance(t *testing.T, s *Service, accountID int64) int64 {
	t.Helper()
	b, err := s.Balance(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDepositAndIdempotency(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	if err := s.Deposit(ctx, 1, "USDT", 1000, "dep-1"); err != nil {
		t.Fatal(err)
	}
	main, _ := s.UserAccount(ctx, 1, "USDT", KindMain)
	if got := mustBalance(t, s, main); got != 1000 {
		t.Fatalf("balance after deposit: %d", got)
	}
	// Replaying the exact same deposit must be a silent no-op.
	if err := s.Deposit(ctx, 1, "USDT", 1000, "dep-1"); err != nil {
		t.Fatal(err)
	}
	if got := mustBalance(t, s, main); got != 1000 {
		t.Fatalf("idempotency violated: balance %d", got)
	}
	if err := s.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOverdraftImpossible(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	s.Deposit(ctx, 1, "USDT", 100, "dep-1")
	err := s.Lock(ctx, 1, "USDT", 500, "lock-1")
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	// The failed posting must leave no partial state behind.
	if err := s.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
	main, _ := s.UserAccount(ctx, 1, "USDT", KindMain)
	if got := mustBalance(t, s, main); got != 100 {
		t.Fatalf("balance disturbed by failed lock: %d", got)
	}
}

func TestUnbalancedPostRejected(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	main, _ := s.UserAccount(ctx, 1, "USDT", KindMain)
	err := s.Post(ctx, "trade", "bad-1", []Entry{{AccountID: main, Asset: "USDT", Amount: 5}})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("expected ErrUnbalanced, got %v", err)
	}
}

// TestFullTradeSettlement walks the money through the whole lifecycle:
// deposit -> lock on order placement -> trade settlement, then verifies every
// balance and the global invariants, including idempotent replay.
func TestFullTradeSettlement(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	const buyer, seller = 1, 2

	// Funding.
	if err := s.Deposit(ctx, buyer, "USDT", 1000, "dep-b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Deposit(ctx, seller, "BTC", 10, "dep-s"); err != nil {
		t.Fatal(err)
	}

	// Order placement reserves funds: buyer bids 10 BTC at price 50 (quote
	// units per base unit) -> locks 500 USDT; seller asks 10 BTC -> locks 10 BTC.
	if err := s.Lock(ctx, buyer, "USDT", 500, "lock-b1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(ctx, seller, "BTC", 10, "lock-s1"); err != nil {
		t.Fatal(err)
	}

	// The engine reports the match (taker = buyer).
	ev := engine.Event{
		Type: engine.EvtTrade, Seq: 7, Index: 1, Symbol: "BTC-USDT",
		Side: orderbook.Buy, Price: 50, Qty: 10,
		MakerUserID: seller, TakerUserID: buyer,
	}
	if err := s.SettleTrade(ctx, ev); err != nil {
		t.Fatal(err)
	}
	// Crash/replay: settling the same event again must change nothing.
	if err := s.SettleTrade(ctx, ev); err != nil {
		t.Fatal(err)
	}

	check := func(userID int64, asset, kind string, want int64) {
		t.Helper()
		id, _ := s.UserAccount(ctx, userID, asset, kind)
		if got := mustBalance(t, s, id); got != want {
			t.Errorf("user %d %s/%s = %d, want %d", userID, asset, kind, got, want)
		}
	}
	check(buyer, "USDT", KindMain, 500)  // 1000 deposited - 500 locked
	check(buyer, "USDT", KindLocked, 0)  // fully spent on the fill
	check(buyer, "BTC", KindMain, 10)    // received
	check(seller, "BTC", KindMain, 0)    // all 10 were locked and sold
	check(seller, "BTC", KindLocked, 0)  // fully delivered
	check(seller, "USDT", KindMain, 500) // proceeds

	if err := s.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}

	// Conservation: each asset nets to zero across ALL accounts (system included).
	var net int64
	for _, asset := range []string{"BTC", "USDT"} {
		if err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(balance),0) FROM accounts WHERE asset = $1`, asset).Scan(&net); err != nil {
			t.Fatal(err)
		}
		if net != 0 {
			t.Errorf("asset %s does not net to zero: %d", asset, net)
		}
	}
}
