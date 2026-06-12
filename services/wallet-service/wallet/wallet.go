// Package wallet manages deposits and withdrawals, bridging on-chain activity
// (via a custody provider) and the ledger.
//
// Deposit flow:  custody address -> chain deposit -> confirmation webhook ->
//
//	once final, credit the user's ledger balance (idempotent on tx hash).
//
// Withdrawal flow (defence in depth, the part attackers target):
//  1. Request: enforce 2FA, require the destination be on the user's
//     allowlist, enforce a per-request cap, and LOCK the funds in the ledger.
//  2. Approve: an operator (or automated policy) releases it; only then does
//     the custody provider broadcast and the ledger settle locked -> system.
//  3. Reject: the locked funds are returned to the user.
package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/custody"
)

var (
	// ErrNotAllowlisted is returned when withdrawing to an unapproved address.
	ErrNotAllowlisted = errors.New("wallet: destination address is not allowlisted")
	// ErrOverLimit is returned when a withdrawal exceeds the per-request cap.
	ErrOverLimit = errors.New("wallet: amount exceeds withdrawal limit")
	// ErrBadAmount is returned for non-positive amounts.
	ErrBadAmount = errors.New("wallet: amount must be positive")
	// ErrNotFound is returned for unknown withdrawals.
	ErrNotFound = errors.New("wallet: not found")
	// ErrNotPending is returned when deciding a withdrawal that is not pending.
	ErrNotPending = errors.New("wallet: withdrawal is not in requested state")
)

// ConfirmationsRequired is how many on-chain confirmations a deposit needs
// before it is credited. Production sets this per-asset; the MVP uses one value.
const ConfirmationsRequired = 3

// Service is the wallet business logic over Postgres + a custody provider + the
// ledger.
type Service struct {
	pool          *pgxpool.Pool
	custody       custody.Provider
	ledger        *ledger.Service
	withdrawLimit int64 // max units per single withdrawal request
}

// New builds a wallet service. withdrawLimit caps a single withdrawal request.
func New(pool *pgxpool.Pool, prov custody.Provider, led *ledger.Service, withdrawLimit int64) *Service {
	return &Service{pool: pool, custody: prov, ledger: led, withdrawLimit: withdrawLimit}
}

// DepositAddress returns the user's deposit address for an asset, creating one
// via the custody provider on first use.
func (s *Service) DepositAddress(ctx context.Context, userID int64, asset string) (string, error) {
	var addr string
	err := s.pool.QueryRow(ctx,
		`SELECT address FROM deposit_addresses WHERE user_id = $1 AND asset = $2`,
		userID, asset).Scan(&addr)
	if err == nil {
		return addr, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	addr, err = s.custody.NewAddress(ctx, asset, fmt.Sprintf("user:%d", userID))
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO deposit_addresses (user_id, asset, address) VALUES ($1, $2, $3)`,
		userID, asset, addr)
	// Lost a race to create the row: read back the winner.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		err = s.pool.QueryRow(ctx,
			`SELECT address FROM deposit_addresses WHERE user_id = $1 AND asset = $2`,
			userID, asset).Scan(&addr)
	}
	return addr, err
}

// HandleDeposit processes a custody confirmation webhook for an address. It
// records/updates the deposit and, once it has enough confirmations, credits
// the ledger exactly once (keyed by tx hash). Returns true if it credited.
func (s *Service) HandleDeposit(ctx context.Context, asset, address, txHash string, amount int64, confirmations int) (credited bool, err error) {
	if amount <= 0 {
		return false, ErrBadAmount
	}
	var userID int64
	err = s.pool.QueryRow(ctx,
		`SELECT user_id FROM deposit_addresses WHERE address = $1 AND asset = $2`,
		address, asset).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("wallet: deposit to unknown address %s", address)
	}
	if err != nil {
		return false, err
	}

	// Upsert the deposit row, tracking confirmation count.
	var status string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO deposits (user_id, asset, amount, tx_hash, confirmations)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tx_hash, asset)
		DO UPDATE SET confirmations = GREATEST(deposits.confirmations, EXCLUDED.confirmations)
		RETURNING status`, userID, asset, amount, txHash, confirmations).Scan(&status)
	if err != nil {
		return false, err
	}
	if status == "credited" || confirmations < ConfirmationsRequired {
		return false, nil
	}

	// Credit the ledger (idempotent) and flip the deposit to credited.
	if err := s.ledger.Deposit(ctx, userID, asset, amount, "deposit-"+txHash); err != nil {
		return false, err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE deposits SET status = 'credited' WHERE tx_hash = $1 AND asset = $2`,
		txHash, asset)
	return err == nil, err
}

// AddAllowlistAddress registers a withdrawal destination. Callers must verify
// the user's 2FA before invoking this.
func (s *Service) AddAllowlistAddress(ctx context.Context, userID int64, asset, address, label string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO withdrawal_allowlist (user_id, asset, address, label)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, asset, address) DO NOTHING`,
		userID, asset, address, label)
	return err
}

// Withdrawal is a withdrawal record.
type Withdrawal struct {
	ID      int64  `json:"id"`
	UserID  int64  `json:"user_id"`
	Asset   string `json:"asset"`
	Amount  int64  `json:"amount"`
	Address string `json:"address"`
	Status  string `json:"status"`
	TxHash  string `json:"tx_hash,omitempty"`
}

// RequestWithdrawal validates and locks funds for a withdrawal. The caller must
// have already verified 2FA. The destination must be allowlisted and the amount
// within the per-request limit; the funds are locked pending approval.
func (s *Service) RequestWithdrawal(ctx context.Context, userID int64, asset, address string, amount int64) (*Withdrawal, error) {
	if amount <= 0 {
		return nil, ErrBadAmount
	}
	if amount > s.withdrawLimit {
		return nil, ErrOverLimit
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM withdrawal_allowlist WHERE user_id = $1 AND asset = $2 AND address = $3)`,
		userID, asset, address).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotAllowlisted
	}

	w := &Withdrawal{UserID: userID, Asset: asset, Amount: amount, Address: address, Status: "requested"}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO withdrawals (user_id, asset, amount, address) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, asset, amount, address).Scan(&w.ID); err != nil {
		return nil, err
	}
	// Lock the funds so they can't be spent or double-withdrawn while pending.
	if err := s.ledger.Lock(ctx, userID, asset, amount, fmt.Sprintf("wlock-%d", w.ID)); err != nil {
		// Roll the request back so a locked-fund failure leaves no dangling row.
		s.pool.Exec(ctx, `DELETE FROM withdrawals WHERE id = $1`, w.ID)
		return nil, err
	}
	return w, nil
}

// PendingWithdrawals lists withdrawals awaiting a decision (operator view).
func (s *Service) PendingWithdrawals(ctx context.Context) ([]Withdrawal, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, asset, amount, address, status FROM withdrawals
		 WHERE status = 'requested' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Withdrawal
	for rows.Next() {
		var w Withdrawal
		if err := rows.Scan(&w.ID, &w.UserID, &w.Asset, &w.Amount, &w.Address, &w.Status); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ApproveWithdrawal broadcasts an approved withdrawal via custody and settles
// the ledger (locked -> system). Idempotent on the withdrawal id.
func (s *Service) ApproveWithdrawal(ctx context.Context, id int64) (*Withdrawal, error) {
	w, err := s.lockRequested(ctx, id)
	if err != nil {
		return nil, err
	}
	// Settle the ledger first (locked funds leave the books), keyed by id so a
	// retry can't double-settle.
	if err := s.ledger.SettleWithdrawal(ctx, w.UserID, w.Asset, w.Amount, fmt.Sprintf("wsettle-%d", id)); err != nil {
		return nil, err
	}
	txHash, err := s.custody.Withdraw(ctx, w.Asset, w.Address, w.Amount, fmt.Sprintf("withdraw-%d", id))
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE withdrawals SET status = 'broadcast', tx_hash = $2, decided_at = $3 WHERE id = $1`,
		id, txHash, time.Now())
	if err != nil {
		return nil, err
	}
	w.Status, w.TxHash = "broadcast", txHash
	return w, nil
}

// RejectWithdrawal denies a pending withdrawal and returns the locked funds.
func (s *Service) RejectWithdrawal(ctx context.Context, id int64) (*Withdrawal, error) {
	w, err := s.lockRequested(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.ledger.Unlock(ctx, w.UserID, w.Asset, w.Amount, fmt.Sprintf("wunlock-%d", id)); err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE withdrawals SET status = 'rejected', decided_at = $2 WHERE id = $1`, id, time.Now())
	if err != nil {
		return nil, err
	}
	w.Status = "rejected"
	return w, nil
}

// lockRequested reads a withdrawal and ensures it is still in 'requested'
// state, using SELECT ... FOR UPDATE to serialize concurrent approve/reject.
func (s *Service) lockRequested(ctx context.Context, id int64) (*Withdrawal, error) {
	var w Withdrawal
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, asset, amount, address, status FROM withdrawals
		 WHERE id = $1 FOR UPDATE`, id).Scan(&w.ID, &w.UserID, &w.Asset, &w.Amount, &w.Address, &w.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if w.Status != "requested" {
		return nil, ErrNotPending
	}
	return &w, nil
}
