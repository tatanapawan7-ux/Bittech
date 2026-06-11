// Package ledger implements the exchange's double-entry ledger: the single
// source of truth for who owns what.
//
// Invariants (enforced here and checkable in SQL):
//   - Every transaction's entries sum to zero per asset (double entry). The
//     external world is modelled as system accounts, so the sum of ALL account
//     balances per asset is always exactly zero.
//   - User accounts can never go negative; an UPDATE guard makes overdrafts
//     impossible even under concurrent settlement.
//   - Posting is idempotent: each transaction carries a caller-supplied
//     idempotency key (for trades, the engine's journal (seq,index)), so
//     replaying an event stream after a crash cannot double-post.
//
// Flow: placing an order LOCKs funds (main -> locked); a cancel UNLOCKs them;
// a trade settles locked funds across the two parties.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

var (
	// ErrUnbalanced is returned when a posting's entries don't sum to zero per asset.
	ErrUnbalanced = errors.New("ledger: entries do not balance")
	// ErrInsufficientFunds is returned when a posting would overdraw a user account.
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
	// ErrBadSymbol is returned for symbols not of the form BASE-QUOTE.
	ErrBadSymbol = errors.New("ledger: malformed symbol")
)

// Account kinds. Main holds spendable balance; locked holds balance reserved
// for open orders; system accounts are the external-world counterparties.
const (
	KindMain   = "main"
	KindLocked = "locked"
	KindSystem = "system"
)

// Entry is one leg of a posting: a signed amount applied to an account.
type Entry struct {
	AccountID int64
	Asset     string
	Amount    int64 // debit < 0, credit > 0, in the asset's smallest unit
}

// Service posts to the ledger through a Postgres pool.
type Service struct {
	pool *pgxpool.Pool
}

// New wraps an existing connection pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// UserAccount returns (creating if needed) the account id for a user's
// (asset, kind) bucket.
func (s *Service) UserAccount(ctx context.Context, userID int64, asset, kind string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (user_id, asset, kind) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, asset, kind) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING id`, userID, asset, kind).Scan(&id)
	return id, err
}

// SystemAccount returns (creating if needed) the system account for an asset —
// the contra-account representing the external world (deposits/withdrawals).
func (s *Service) SystemAccount(ctx context.Context, asset string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (user_id, asset, kind) VALUES (NULL, $1, $2)
		ON CONFLICT (asset, kind) WHERE user_id IS NULL DO UPDATE SET asset = EXCLUDED.asset
		RETURNING id`, asset, KindSystem).Scan(&id)
	return id, err
}

// Balance returns the current balance of an account.
func (s *Service) Balance(ctx context.Context, accountID int64) (int64, error) {
	var b int64
	err := s.pool.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, accountID).Scan(&b)
	return b, err
}

// Post atomically applies one balanced transaction. If idempotencyKey was
// already posted, Post is a no-op and returns nil.
func (s *Service) Post(ctx context.Context, kind, idempotencyKey string, entries []Entry) error {
	// Validate double-entry balance per asset before touching the database.
	sums := map[string]int64{}
	for _, e := range entries {
		sums[e.Asset] += e.Amount
	}
	for asset, sum := range sums {
		if sum != 0 {
			return fmt.Errorf("%w: %s nets to %d", ErrUnbalanced, asset, sum)
		}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var txID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO transactions (kind, idempotency_key) VALUES ($1, $2)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id`, kind, idempotencyKey).Scan(&txID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already posted: idempotent success
	}
	if err != nil {
		return err
	}

	for _, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO entries (transaction_id, account_id, asset, amount)
			VALUES ($1, $2, $3, $4)`, txID, e.AccountID, e.Asset, e.Amount); err != nil {
			return err
		}
		// The guard makes user-account overdrafts impossible; system accounts
		// (the external world) are allowed to go negative.
		tag, err := tx.Exec(ctx, `
			UPDATE accounts SET balance = balance + $1
			WHERE id = $2 AND (kind = 'system' OR balance + $1 >= 0)`, e.Amount, e.AccountID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: account %d short by %d %s", ErrInsufficientFunds, e.AccountID, -e.Amount, e.Asset)
		}
	}
	return tx.Commit(ctx)
}

// Deposit credits a user's main account from the asset's system account.
// (Called by the wallet service once a chain deposit is confirmed.)
func (s *Service) Deposit(ctx context.Context, userID int64, asset string, amount int64, idempotencyKey string) error {
	if amount <= 0 {
		return fmt.Errorf("%w: non-positive deposit", ErrUnbalanced)
	}
	sys, err := s.SystemAccount(ctx, asset)
	if err != nil {
		return err
	}
	main, err := s.UserAccount(ctx, userID, asset, KindMain)
	if err != nil {
		return err
	}
	return s.Post(ctx, "deposit", idempotencyKey, []Entry{
		{AccountID: sys, Asset: asset, Amount: -amount},
		{AccountID: main, Asset: asset, Amount: +amount},
	})
}

// Lock reserves funds for an open order: main -> locked. Fails with
// ErrInsufficientFunds if the spendable balance is too small.
func (s *Service) Lock(ctx context.Context, userID int64, asset string, amount int64, idempotencyKey string) error {
	return s.transfer(ctx, "lock", idempotencyKey, userID, asset, KindMain, KindLocked, amount)
}

// Unlock releases reserved funds on cancel: locked -> main.
func (s *Service) Unlock(ctx context.Context, userID int64, asset string, amount int64, idempotencyKey string) error {
	return s.transfer(ctx, "unlock", idempotencyKey, userID, asset, KindLocked, KindMain, amount)
}

func (s *Service) transfer(ctx context.Context, kind, key string, userID int64, asset, fromKind, toKind string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: non-positive transfer", ErrUnbalanced)
	}
	from, err := s.UserAccount(ctx, userID, asset, fromKind)
	if err != nil {
		return err
	}
	to, err := s.UserAccount(ctx, userID, asset, toKind)
	if err != nil {
		return err
	}
	return s.Post(ctx, kind, key, []Entry{
		{AccountID: from, Asset: asset, Amount: -amount},
		{AccountID: to, Asset: asset, Amount: +amount},
	})
}

// SettleTrade consumes one engine trade event and settles it: the buyer's
// locked quote pays the seller, the seller's locked base delivers to the buyer.
// The idempotency key is derived from the engine's journal position, so
// replaying the event stream is safe.
//
// Price convention: quote smallest-units per base smallest-unit, so
// quoteAmount = price * qty.
func (s *Service) SettleTrade(ctx context.Context, ev engine.Event) error {
	if ev.Type != engine.EvtTrade {
		return fmt.Errorf("ledger: not a trade event: %s", ev.Type)
	}
	base, quote, ok := strings.Cut(ev.Symbol, "-")
	if !ok || base == "" || quote == "" {
		return fmt.Errorf("%w: %q", ErrBadSymbol, ev.Symbol)
	}
	quoteAmt := ev.Price * ev.Qty
	if ev.Price != 0 && quoteAmt/ev.Price != ev.Qty {
		return fmt.Errorf("ledger: quote amount overflow for trade seq=%d", ev.Seq)
	}

	// ev.Side is the taker's side.
	buyer, seller := ev.TakerUserID, ev.MakerUserID
	if ev.Side == orderbook.Sell {
		buyer, seller = ev.MakerUserID, ev.TakerUserID
	}

	buyerLockedQuote, err := s.UserAccount(ctx, buyer, quote, KindLocked)
	if err != nil {
		return err
	}
	buyerMainBase, err := s.UserAccount(ctx, buyer, base, KindMain)
	if err != nil {
		return err
	}
	sellerLockedBase, err := s.UserAccount(ctx, seller, base, KindLocked)
	if err != nil {
		return err
	}
	sellerMainQuote, err := s.UserAccount(ctx, seller, quote, KindMain)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("trade-%d-%d", ev.Seq, ev.Index)
	return s.Post(ctx, "trade", key, []Entry{
		// Quote leg: buyer pays seller.
		{AccountID: buyerLockedQuote, Asset: quote, Amount: -quoteAmt},
		{AccountID: sellerMainQuote, Asset: quote, Amount: +quoteAmt},
		// Base leg: seller delivers to buyer.
		{AccountID: sellerLockedBase, Asset: base, Amount: -ev.Qty},
		{AccountID: buyerMainBase, Asset: base, Amount: +ev.Qty},
	})
}

// CheckInvariants verifies the two global ledger invariants and returns an
// error describing any violation. Run by the reconciliation job.
func (s *Service) CheckInvariants(ctx context.Context) error {
	// 1. No transaction may be unbalanced.
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM unbalanced_transactions`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("ledger: %d unbalanced transactions", n)
	}
	// 2. Cached balances must equal the sum of entries, per account.
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM accounts a
		WHERE a.balance <> COALESCE(
			(SELECT SUM(e.amount) FROM entries e WHERE e.account_id = a.id), 0)`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("ledger: %d accounts with drifted cached balance", n)
	}
	return nil
}
