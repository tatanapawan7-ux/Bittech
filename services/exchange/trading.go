// Package exchange composes the account service, ledger, and matching engine
// into the trading API — the service users actually talk to.
//
// Order lifecycle and money movement:
//
//  1. Place: the required funds are LOCKED in the ledger first (limit buy:
//     price*qty of quote; sell: qty of base). Only then is the command
//     submitted to the engine, so the engine never matches unfunded orders.
//  2. Fills: each trade event settles via the ledger (idempotent on the
//     engine's journal position). When a taker buy fills below its limit
//     price, the price improvement is unlocked back to the buyer.
//  3. Cancel/reject: the funds reserved for the unfilled remainder are
//     unlocked.
//
// In the target architecture steps 2–3 run in a stream consumer on the engine's
// event topic; here they run in-process. Because every ledger posting is
// idempotent and keyed by the engine journal, moving them behind Redpanda later
// does not change any of this logic.
package exchange

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

var (
	// ErrUnsupported is returned for order shapes the MVP doesn't take yet.
	ErrUnsupported = errors.New("exchange: market buy orders are not supported yet (use a limit order)")
	// ErrRejected wraps an engine rejection reason.
	ErrRejected = errors.New("exchange: order rejected")
	// ErrBadOrder is returned for invalid order parameters.
	ErrBadOrder = errors.New("exchange: invalid order")
)

// Trading coordinates the ledger and the matching engine.
type Trading struct {
	eng    *engine.Engine
	ledger *ledger.Service

	// Trades are kept per symbol for the public trade history endpoint.
	mu     sync.Mutex
	trades map[string][]engine.Event
}

// recentTradeCap bounds the in-memory trade history per symbol.
const recentTradeCap = 200

// NewTrading wires the trading coordinator.
func NewTrading(eng *engine.Engine, led *ledger.Service) *Trading {
	return &Trading{eng: eng, ledger: led, trades: make(map[string][]engine.Event)}
}

// PlaceOrder locks funds, submits to the engine, and settles any fills.
// orderType is "limit" or "market". Returns the engine events.
func (t *Trading) PlaceOrder(ctx context.Context, userID int64, symbol, orderType string, side orderbook.Side, price, qty int64) ([]engine.Event, error) {
	base, quote, ok := strings.Cut(symbol, "-")
	if !ok || base == "" || quote == "" {
		return nil, fmt.Errorf("%w: malformed symbol %q", ErrBadOrder, symbol)
	}
	if qty <= 0 || (orderType == "limit" && price <= 0) {
		return nil, fmt.Errorf("%w: non-positive price or qty", ErrBadOrder)
	}

	orderID, err := newID()
	if err != nil {
		return nil, err
	}
	cmd := engine.Command{Symbol: symbol, OrderID: orderID, UserID: userID, Side: side, Qty: qty}

	// Determine what to reserve.
	var lockAsset string
	var lockAmt int64
	switch {
	case orderType == "limit" && side == orderbook.Buy:
		cmd.Type, cmd.Price = engine.CmdPlaceLimit, price
		lockAsset = quote
		lockAmt = price * qty
		if price != 0 && lockAmt/price != qty {
			return nil, fmt.Errorf("%w: notional overflow", ErrBadOrder)
		}
	case orderType == "limit" && side == orderbook.Sell:
		cmd.Type, cmd.Price = engine.CmdPlaceLimit, price
		lockAsset, lockAmt = base, qty
	case orderType == "market" && side == orderbook.Sell:
		cmd.Type = engine.CmdPlaceMarket
		lockAsset, lockAmt = base, qty
	case orderType == "market" && side == orderbook.Buy:
		// A market buy's cost is unknowable upfront; supporting it needs
		// quote-quantity orders (Phase 4 follow-up).
		return nil, ErrUnsupported
	default:
		return nil, fmt.Errorf("%w: unknown order type %q", ErrBadOrder, orderType)
	}

	if err := t.ledger.Lock(ctx, userID, lockAsset, lockAmt, "lock-"+orderID); err != nil {
		return nil, err
	}

	events, err := t.eng.Submit(ctx, cmd)
	if err != nil {
		// Engine unavailable: the journal never saw the command, release the lock.
		_ = t.ledger.Unlock(ctx, userID, lockAsset, lockAmt, "unlock-fail-"+orderID)
		return nil, err
	}
	if err := t.settle(ctx, cmd, events, lockAsset, lockAmt); err != nil {
		return events, err
	}
	if len(events) > 0 && events[0].Type == engine.EvtOrderRejected {
		return events, fmt.Errorf("%w: %s", ErrRejected, events[0].Reason)
	}
	// A market order never rests: whatever didn't fill is dead, so release
	// the lock held for the unfilled remainder.
	if cmd.Type == engine.CmdPlaceMarket {
		var filled int64
		for _, ev := range events {
			if ev.Type == engine.EvtTrade {
				filled += ev.Qty
			}
		}
		if unfilled := qty - filled; unfilled > 0 {
			if err := t.ledger.Unlock(ctx, userID, lockAsset, unfilled, "unlock-unfilled-"+orderID); err != nil {
				return events, err
			}
		}
	}
	return events, nil
}

// CancelOrder cancels a resting order and unlocks the unfilled remainder. The
// engine enforces that only the order's owner may cancel it.
func (t *Trading) CancelOrder(ctx context.Context, userID int64, symbol, orderID string) ([]engine.Event, error) {
	events, err := t.eng.Submit(ctx, engine.Command{
		Type: engine.CmdCancel, Symbol: symbol, OrderID: orderID, UserID: userID,
	})
	if err != nil {
		return nil, err
	}
	if err := t.settle(ctx, engine.Command{Symbol: symbol}, events, "", 0); err != nil {
		return events, err
	}
	if len(events) > 0 && events[0].Type == engine.EvtOrderRejected {
		return events, fmt.Errorf("%w: %s", ErrRejected, events[0].Reason)
	}
	return events, nil
}

// settle applies the ledger consequences of a batch of engine events. Every
// posting key derives from the engine journal position or order id, so calling
// this twice (or replaying after a crash) cannot double-post.
func (t *Trading) settle(ctx context.Context, cmd engine.Command, events []engine.Event, lockAsset string, lockAmt int64) error {
	for _, ev := range events {
		switch ev.Type {
		case engine.EvtTrade:
			if err := t.ledger.SettleTrade(ctx, ev); err != nil {
				return err
			}
			// Taker bought below their limit: release the price improvement.
			if cmd.Type == engine.CmdPlaceLimit && ev.Side == orderbook.Buy && ev.Price < cmd.Price {
				refund := (cmd.Price - ev.Price) * ev.Qty
				key := fmt.Sprintf("improve-%d-%d", ev.Seq, ev.Index)
				if err := t.ledger.Unlock(ctx, ev.TakerUserID, lockAsset, refund, key); err != nil {
					return err
				}
			}
			t.recordTrade(ev)

		case engine.EvtOrderCancelled:
			if ev.UserID == 0 || ev.Qty == 0 {
				continue
			}
			base, quote, _ := strings.Cut(ev.Symbol, "-")
			asset, amt := base, ev.Qty
			if ev.Side == orderbook.Buy {
				asset, amt = quote, ev.Price*ev.Qty
			}
			if err := t.ledger.Unlock(ctx, ev.UserID, asset, amt, "unlock-cancel-"+ev.OrderID); err != nil {
				return err
			}

		case engine.EvtOrderRejected:
			// The lock was taken optimistically before submit; give it back.
			if lockAmt > 0 {
				if err := t.ledger.Unlock(ctx, ev.UserID, lockAsset, lockAmt, "unlock-reject-"+ev.OrderID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (t *Trading) recordTrade(ev engine.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := append(t.trades[ev.Symbol], ev)
	if len(list) > recentTradeCap {
		list = list[len(list)-recentTradeCap:]
	}
	t.trades[ev.Symbol] = list
}

// RecentTrades returns up to recentTradeCap most recent trades for a symbol,
// newest last.
func (t *Trading) RecentTrades(symbol string) []engine.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]engine.Event(nil), t.trades[symbol]...)
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
