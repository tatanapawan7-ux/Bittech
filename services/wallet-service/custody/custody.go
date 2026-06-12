// Package custody abstracts the key-management provider. The exchange never
// holds raw private keys in the MVP: address generation and on-chain
// withdrawals are delegated to a provider (Fireblocks, BitGo, an MPC service).
//
// Provider is the seam. Swapping the mock for a real vendor is an
// implementation of this one interface — nothing else in the wallet service
// changes. This is the single most important "buy, don't build" boundary in
// the system; hand-rolling key custody is how exchanges lose customer funds.
package custody

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// Provider is implemented by custody backends.
type Provider interface {
	// NewAddress returns a fresh deposit address for an asset, tagged with an
	// opaque reference (e.g. "user:42") the provider echoes back on deposits.
	NewAddress(ctx context.Context, asset, ref string) (address string, err error)
	// Withdraw broadcasts an on-chain transfer and returns its tx hash. The
	// idempotencyKey makes retries safe: the same key must never broadcast twice.
	Withdraw(ctx context.Context, asset, address string, amount int64, idempotencyKey string) (txHash string, err error)
}

// Mock is an in-memory Provider for development and tests. It fabricates
// plausible-looking addresses and tx hashes and records broadcasts so tests can
// assert against them. It is deterministic only in structure, not in value.
type Mock struct {
	mu         sync.Mutex
	withdrawn  map[string]string // idempotencyKey -> txHash
	Broadcasts []Broadcast
}

// Broadcast records a withdrawal the mock "sent".
type Broadcast struct {
	Asset   string
	Address string
	Amount  int64
	TxHash  string
}

// NewMock creates an empty mock provider.
func NewMock() *Mock {
	return &Mock{withdrawn: make(map[string]string)}
}

// NewAddress returns a random hex address namespaced by asset.
func (m *Mock) NewAddress(_ context.Context, asset, _ string) (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%s", asset, hex.EncodeToString(b)), nil
}

// Withdraw fabricates a tx hash, returning the same one for a repeated key.
func (m *Mock) Withdraw(_ context.Context, asset, address string, amount int64, idempotencyKey string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tx, ok := m.withdrawn[idempotencyKey]; ok {
		return tx, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tx := "0x" + hex.EncodeToString(b)
	m.withdrawn[idempotencyKey] = tx
	m.Broadcasts = append(m.Broadcasts, Broadcast{Asset: asset, Address: address, Amount: amount, TxHash: tx})
	return tx, nil
}
