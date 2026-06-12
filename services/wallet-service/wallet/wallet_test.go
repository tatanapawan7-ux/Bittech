package wallet

// Integration tests for the wallet service against a real Postgres ledger and a
// mock custody provider. Skipped when Postgres is unreachable.

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
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/custody"
)

type fixture struct {
	wallet  *Service
	ledger  *ledger.Service
	custody *custody.Mock
	pool    *pgxpool.Pool
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
	dbname := fmt.Sprintf("wallet_test_%d", os.Getpid())
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

	files, _ := filepath.Glob("../../../infra/db/*.sql")
	sort.Strings(files)
	for _, f := range files {
		sql, _ := os.ReadFile(f)
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	mustExec(t, pool, `INSERT INTO assets (symbol, name, scale) VALUES ('BTC','Bitcoin',8)`)
	mustExec(t, pool, `INSERT INTO users (email) VALUES ('u1@t.co')`)

	led := ledger.New(pool)
	mock := custody.NewMock()
	return &fixture{
		wallet:  New(pool, mock, led, 1_000_000),
		ledger:  led,
		custody: mock,
		pool:    pool,
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) balance(t *testing.T, userID int64, asset, kind string) int64 {
	t.Helper()
	id, _ := f.ledger.UserAccount(context.Background(), userID, asset, kind)
	b, err := f.ledger.Balance(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDepositAddressIsStable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a1, err := f.wallet.DepositAddress(ctx, 1, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := f.wallet.DepositAddress(ctx, 1, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if a1 == "" || a1 != a2 {
		t.Fatalf("address not stable: %q vs %q", a1, a2)
	}
}

func TestDepositCreditsOnceWhenConfirmed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	addr, _ := f.wallet.DepositAddress(ctx, 1, "BTC")

	// Below the confirmation threshold: recorded but not credited.
	credited, err := f.wallet.HandleDeposit(ctx, "BTC", addr, "tx1", 500, 1)
	if err != nil || credited {
		t.Fatalf("premature credit: credited=%v err=%v", credited, err)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindMain); got != 0 {
		t.Fatalf("balance should be 0, got %d", got)
	}

	// Reaches the threshold: credited exactly once.
	credited, err = f.wallet.HandleDeposit(ctx, "BTC", addr, "tx1", 500, ConfirmationsRequired)
	if err != nil || !credited {
		t.Fatalf("expected credit: credited=%v err=%v", credited, err)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindMain); got != 500 {
		t.Fatalf("balance: %d", got)
	}

	// A duplicate confirmation webhook must not double-credit.
	credited, err = f.wallet.HandleDeposit(ctx, "BTC", addr, "tx1", 500, ConfirmationsRequired+2)
	if err != nil || credited {
		t.Fatalf("double credit: credited=%v err=%v", credited, err)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindMain); got != 500 {
		t.Fatalf("balance after duplicate: %d", got)
	}
	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDepositToUnknownAddressFails(t *testing.T) {
	f := setup(t)
	_, err := f.wallet.HandleDeposit(context.Background(), "BTC", "BTC_nope", "tx9", 100, 10)
	if err == nil {
		t.Fatal("expected error for unknown address")
	}
}

func fund(t *testing.T, f *fixture, amount int64) {
	t.Helper()
	addr, _ := f.wallet.DepositAddress(context.Background(), 1, "BTC")
	if _, err := f.wallet.HandleDeposit(context.Background(), "BTC", addr, "fund-tx", amount, ConfirmationsRequired); err != nil {
		t.Fatal(err)
	}
}

func TestWithdrawalRequiresAllowlist(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fund(t, f, 1000)
	_, err := f.wallet.RequestWithdrawal(ctx, 1, "BTC", "BTC_dest", 100)
	if !errors.Is(err, ErrNotAllowlisted) {
		t.Fatalf("expected ErrNotAllowlisted, got %v", err)
	}
}

func TestWithdrawalRespectsLimit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fund(t, f, 5_000_000)
	f.wallet.AddAllowlistAddress(ctx, 1, "BTC", "BTC_dest", "cold")
	_, err := f.wallet.RequestWithdrawal(ctx, 1, "BTC", "BTC_dest", 2_000_000)
	if !errors.Is(err, ErrOverLimit) {
		t.Fatalf("expected ErrOverLimit, got %v", err)
	}
}

func TestWithdrawalApprovalFlow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fund(t, f, 1000)
	f.wallet.AddAllowlistAddress(ctx, 1, "BTC", "BTC_dest", "cold")

	w, err := f.wallet.RequestWithdrawal(ctx, 1, "BTC", "BTC_dest", 300)
	if err != nil {
		t.Fatal(err)
	}
	// Funds are locked, not yet gone.
	if got := f.balance(t, 1, "BTC", ledger.KindLocked); got != 300 {
		t.Fatalf("locked: %d", got)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindMain); got != 700 {
		t.Fatalf("main: %d", got)
	}

	approved, err := f.wallet.ApproveWithdrawal(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "broadcast" || approved.TxHash == "" {
		t.Fatalf("approval: %+v", approved)
	}
	// Funds have left the books entirely.
	if got := f.balance(t, 1, "BTC", ledger.KindLocked); got != 0 {
		t.Fatalf("locked after broadcast: %d", got)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindMain); got != 700 {
		t.Fatalf("main after broadcast: %d", got)
	}
	// Custody actually broadcast it.
	if len(f.custody.Broadcasts) != 1 || f.custody.Broadcasts[0].Amount != 300 {
		t.Fatalf("custody broadcasts: %+v", f.custody.Broadcasts)
	}
	// Approving again is rejected (not pending) and does not re-broadcast.
	if _, err := f.wallet.ApproveWithdrawal(ctx, w.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected ErrNotPending, got %v", err)
	}
	if len(f.custody.Broadcasts) != 1 {
		t.Fatalf("double broadcast: %+v", f.custody.Broadcasts)
	}
	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWithdrawalRejectionRefunds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fund(t, f, 1000)
	f.wallet.AddAllowlistAddress(ctx, 1, "BTC", "BTC_dest", "cold")

	w, _ := f.wallet.RequestWithdrawal(ctx, 1, "BTC", "BTC_dest", 300)
	if _, err := f.wallet.RejectWithdrawal(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindMain); got != 1000 {
		t.Fatalf("refund failed, main: %d", got)
	}
	if got := f.balance(t, 1, "BTC", ledger.KindLocked); got != 0 {
		t.Fatalf("locked after reject: %d", got)
	}
	if len(f.custody.Broadcasts) != 0 {
		t.Fatal("rejected withdrawal must not broadcast")
	}
	if err := f.ledger.CheckInvariants(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPendingWithdrawalsListed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fund(t, f, 1000)
	f.wallet.AddAllowlistAddress(ctx, 1, "BTC", "BTC_dest", "cold")
	f.wallet.RequestWithdrawal(ctx, 1, "BTC", "BTC_dest", 100)
	f.wallet.RequestWithdrawal(ctx, 1, "BTC", "BTC_dest", 200)
	pending, err := f.wallet.PendingWithdrawals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
}
