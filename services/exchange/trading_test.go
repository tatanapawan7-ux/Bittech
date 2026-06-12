package exchange

// Integration tests for the trading coordinator: real Postgres ledger + real
// engine, exercising the money flow around order placement, fills, cancels,
// and refunds. Skipped when Postgres is unreachable (set TEST_PG_URL to
// override the default local connection).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

const sym = "BTC-USDT"

type fixture struct {
	trading *Trading
	ledger  *ledger.Service
}

func setup(t *testing.T) *fixture {
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
	dbname := fmt.Sprintf("exchange_test_%d", os.Getpid())
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

	files, _ := filepath.Glob("../../infra/db/*.sql")
	if len(files) == 0 {
		t.Fatal("no migrations found")
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
	mustExec(t, pool, `INSERT INTO assets (symbol, name, scale) VALUES ('BTC','Bitcoin',8), ('USDT','Tether',6) ON CONFLICT (symbol) DO NOTHING`)
	mustExec(t, pool, `INSERT INTO users (email) VALUES ('u1@t.co'), ('u2@t.co')`)

	led := ledger.New(pool)
	eng, err := engine.New([]string{sym}, &engine.MemJournal{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ectx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go eng.Run(ectx)
	return &fixture{trading: NewTrading(eng, led), ledger: led}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) balance(t *testing.T, userID int64, asset, kind string) int64 {
	t.Helper()
	id, err := f.ledger.UserAccount(context.Background(), userID, asset, kind)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.ledger.Balance(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fixture) check(t *testing.T, userID int64, asset, kind string, want int64) {
	t.Helper()
	if got := f.balance(t, userID, asset, kind); got != want {
		t.Errorf("user %d %s/%s = %d, want %d", userID, asset, kind, got, want)
	}
}

func TestPlaceOrderRequiresFunds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.trading.PlaceOrder(ctx, 1, sym, "limit", orderbook.Buy, 50, 10)
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
}

// TestFullTradeFlow: seller posts an ask, buyer crosses it; both sides' funds
// move correctly and the ledger invariants hold.
func TestFullTradeFlow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const buyer, seller = 1, 2

	if err := f.ledger.Deposit(ctx, buyer, "USDT", 1000, "dep-b"); err != nil {
		t.Fatal(err)
	}
	if err := f.ledger.Deposit(ctx, seller, "BTC", 10, "dep-s"); err != nil {
		t.Fatal(err)
	}

	// Seller asks 10 BTC @ 50.
	evs, err := f.trading.PlaceOrder(ctx, seller, sym, "limit", orderbook.Sell, 50, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("ask: %v %+v", err, evs)
	}
	f.check(t, seller, "BTC", ledger.KindLocked, 10) // reserved

	// Buyer lifts the whole ask.
	evs, err = f.trading.PlaceOrder(ctx, buyer, sym, "limit", orderbook.Buy, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	var trades int
	for _, ev := range evs {
		if ev.Type == engine.EvtTrade {
			trades++
		}
	}
	if trades != 1 {
		t.Fatalf("expected 1 trade, got %d in %+v", trades, evs)
	}

	f.check(t, buyer, "USDT", ledger.KindMain, 500) // 1000 - 500 spent
	f.check(t, buyer, "USDT", ledger.KindLocked, 0)
	f.check(t, buyer, "BTC", ledger.KindMain, 10)
	f.check(t, seller, "BTC", ledger.KindMain, 0)
	f.check(t, seller, "BTC", ledger.KindLocked, 0)
	f.check(t, seller, "USDT", ledger.KindMain, 500)

	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.trading.RecentTrades(sym)); got != 1 {
		t.Fatalf("trade history: %d", got)
	}
}

// TestPriceImprovementRefund: a taker bidding above the resting ask pays the
// ask price; the difference must be unlocked back.
func TestPriceImprovementRefund(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const buyer, seller = 1, 2
	f.ledger.Deposit(ctx, buyer, "USDT", 1000, "dep-b")
	f.ledger.Deposit(ctx, seller, "BTC", 10, "dep-s")

	// Ask at 40; buyer bids 50 for 10 -> locks 500, executes at 40 (=400).
	if _, err := f.trading.PlaceOrder(ctx, seller, sym, "limit", orderbook.Sell, 40, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.trading.PlaceOrder(ctx, buyer, sym, "limit", orderbook.Buy, 50, 10); err != nil {
		t.Fatal(err)
	}
	f.check(t, buyer, "USDT", ledger.KindMain, 600) // 1000 - 400 actually paid
	f.check(t, buyer, "USDT", ledger.KindLocked, 0) // improvement refunded
	f.check(t, seller, "USDT", ledger.KindMain, 400)
	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestCancelUnlocksRemainder: cancelling a partially filled bid releases only
// the unfilled portion of the lock.
func TestCancelUnlocksRemainder(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const buyer, seller = 1, 2
	f.ledger.Deposit(ctx, buyer, "USDT", 1000, "dep-b")
	f.ledger.Deposit(ctx, seller, "BTC", 10, "dep-s")

	// Buyer bids 10 @ 50 (locks 500); seller fills 4 of it at 50.
	evs, err := f.trading.PlaceOrder(ctx, buyer, sym, "limit", orderbook.Buy, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	bidID := evs[0].OrderID
	if _, err := f.trading.PlaceOrder(ctx, seller, sym, "limit", orderbook.Sell, 50, 4); err != nil {
		t.Fatal(err)
	}
	f.check(t, buyer, "USDT", ledger.KindLocked, 300) // 6 remaining * 50

	if _, err := f.trading.CancelOrder(ctx, buyer, sym, bidID); err != nil {
		t.Fatal(err)
	}
	f.check(t, buyer, "USDT", ledger.KindLocked, 0)
	f.check(t, buyer, "USDT", ledger.KindMain, 500+300) // 1000 - 200 spent
	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestCancelOwnershipEnforced: a user cannot cancel someone else's order, and
// the victim's lock is untouched.
func TestCancelOwnershipEnforced(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.ledger.Deposit(ctx, 1, "USDT", 1000, "dep-b")

	evs, err := f.trading.PlaceOrder(ctx, 1, sym, "limit", orderbook.Buy, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.trading.CancelOrder(ctx, 2, sym, evs[0].OrderID); !errors.Is(err, ErrRejected) {
		t.Fatalf("expected rejection, got %v", err)
	}
	f.check(t, 1, "USDT", ledger.KindLocked, 500) // still reserved
}

// TestMarketSellUnfilledUnlock: a market sell into a thin book fills what it
// can; the unfilled remainder's lock is released immediately.
func TestMarketSellUnfilledUnlock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const buyer, seller = 1, 2
	f.ledger.Deposit(ctx, buyer, "USDT", 1000, "dep-b")
	f.ledger.Deposit(ctx, seller, "BTC", 10, "dep-s")

	// Only 3 BTC of bids on the book; seller market-sells 10.
	if _, err := f.trading.PlaceOrder(ctx, buyer, sym, "limit", orderbook.Buy, 50, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.trading.PlaceOrder(ctx, seller, sym, "market", orderbook.Sell, 0, 10); err != nil {
		t.Fatal(err)
	}
	f.check(t, seller, "BTC", ledger.KindLocked, 0) // 3 sold, 7 released
	f.check(t, seller, "BTC", ledger.KindMain, 7)
	f.check(t, seller, "USDT", ledger.KindMain, 150)
	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMarketBuyUnsupported(t *testing.T) {
	f := setup(t)
	_, err := f.trading.PlaceOrder(context.Background(), 1, sym, "market", orderbook.Buy, 0, 5)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
