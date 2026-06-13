// Package engine wraps the order book in an event-sourced matching engine.
//
// Architecture (the pattern used by production exchanges):
//
//	commands ──▶ journal (write-ahead, ordered) ──▶ apply to order book ──▶ events
//
// Every command is durably appended to an ordered journal BEFORE it is applied.
// Because the order book is deterministic, replaying the journal from the start
// reproduces the exact same book state and event stream after a crash — the
// journal, not the in-memory book, is the source of truth. The Journal interface
// is satisfied by a local append-only file today and by a Kafka/Redpanda topic
// in production; the engine does not care which.
//
// A single goroutine owns the engine (single-writer). Submit is safe to call
// from many goroutines; commands are funneled through one channel, which is
// what establishes the total order.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

// CommandType enumerates the engine's inputs.
type CommandType string

// EventType enumerates the engine's outputs.
type EventType string

const (
	CmdPlaceLimit  CommandType = "place_limit"
	CmdPlaceMarket CommandType = "place_market"
	CmdCancel      CommandType = "cancel"

	EvtOrderAccepted  EventType = "order_accepted"
	EvtOrderRejected  EventType = "order_rejected"
	EvtOrderCancelled EventType = "order_cancelled"
	EvtTrade          EventType = "trade"
)

// Command is one instruction to the engine. Exactly the fields needed by Type
// are set. UserID travels with the command so downstream consumers (ledger,
// user streams) can attribute events without a lookup.
type Command struct {
	Type    CommandType    `json:"type"`
	Symbol  string         `json:"symbol"`
	OrderID string         `json:"order_id"`
	UserID  int64          `json:"user_id,omitempty"`
	Side    orderbook.Side `json:"side,omitempty"`
	Price   int64          `json:"price,omitempty"`
	Qty     int64          `json:"qty,omitempty"`
}

// Event is one fact emitted by the engine. Seq is the journal sequence of the
// command that produced it; (Seq, Index) uniquely identifies an event and serves
// as the idempotency key for downstream consumers like the ledger.
type Event struct {
	Type   EventType `json:"type"`
	Seq    uint64    `json:"seq"`
	Index  int       `json:"index"`
	Symbol string    `json:"symbol"`

	OrderID string         `json:"order_id,omitempty"`
	UserID  int64          `json:"user_id,omitempty"`
	Side    orderbook.Side `json:"side,omitempty"`
	Price   int64          `json:"price,omitempty"`
	Qty     int64          `json:"qty,omitempty"`
	Reason  string         `json:"reason,omitempty"`

	// Trade-specific attribution.
	MakerOrderID string `json:"maker_order_id,omitempty"`
	TakerOrderID string `json:"taker_order_id,omitempty"`
	MakerUserID  int64  `json:"maker_user_id,omitempty"`
	TakerUserID  int64  `json:"taker_user_id,omitempty"`
}

// Journal is the ordered, durable command log the engine recovers from.
type Journal interface {
	// Append durably writes one command and returns its sequence number.
	// Sequences are contiguous and start at 1.
	Append(cmd Command) (uint64, error)
	// Replay invokes fn for every journaled command in order.
	Replay(fn func(seq uint64, cmd Command) error) error
}

// Sink receives the engine's output events. Implementations must be fast or
// buffer internally; the engine loop blocks on them.
type Sink func(events []Event)

// Engine is the single-writer matching core for a set of symbols.
type Engine struct {
	books  map[string]*orderbook.OrderBook
	orders map[string]ownedOrder // RESTING order id -> attribution
	// booksMu guards books + orders. There is exactly one writer (apply, run
	// from the engine loop) and many readers (Depth/OpenOrders from HTTP
	// handlers), so a RWMutex is the right fit and keeps reads race-free.
	booksMu sync.RWMutex
	halted  map[string]bool // symbols where new orders are paused
	haltMu  sync.RWMutex    // guards halted (written by control plane)
	journal Journal
	sink    Sink
	cmdCh   chan submitReq
}

// OpenOrder is a resting order belonging to a user, for the open-orders view.
type OpenOrder struct {
	OrderID   string         `json:"order_id"`
	Symbol    string         `json:"symbol"`
	Side      orderbook.Side `json:"side"`
	Price     int64          `json:"price"`
	Quantity  int64          `json:"quantity"`
	Remaining int64          `json:"remaining"`
}

type ownedOrder struct {
	userID int64
	side   orderbook.Side
	symbol string
}

type submitReq struct {
	cmd   Command
	reply chan submitResp
}

type submitResp struct {
	events []Event
	err    error
}

// ErrUnknownSymbol is returned for commands on symbols the engine doesn't trade.
var ErrUnknownSymbol = errors.New("engine: unknown symbol")

// New builds an engine for the given symbols and recovers state by replaying
// the journal. The sink is NOT called for replayed events: downstream systems
// have their own offsets into the event stream.
func New(symbols []string, journal Journal, sink Sink) (*Engine, error) {
	e := &Engine{
		books:   make(map[string]*orderbook.OrderBook, len(symbols)),
		orders:  make(map[string]ownedOrder),
		halted:  make(map[string]bool),
		journal: journal,
		sink:    sink,
		cmdCh:   make(chan submitReq, 1024),
	}
	for _, s := range symbols {
		e.books[s] = orderbook.New(s)
	}
	if err := journal.Replay(func(seq uint64, cmd Command) error {
		e.apply(seq, cmd)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("engine: replay failed: %w", err)
	}
	return e, nil
}

// Run drives the engine loop until ctx is cancelled. It must be running for
// Submit to make progress.
func (e *Engine) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-e.cmdCh:
			// Admission control: when a symbol is halted, reject NEW orders
			// before they are journaled (cancels are always allowed so users
			// can pull resting orders). Because halted orders never enter the
			// journal, replay remains deterministic without journaling halts.
			if req.cmd.Type != CmdCancel && e.IsHalted(req.cmd.Symbol) {
				req.reply <- submitResp{events: []Event{{
					Type: EvtOrderRejected, Symbol: req.cmd.Symbol,
					OrderID: req.cmd.OrderID, UserID: req.cmd.UserID,
					Reason: "trading halted",
				}}}
				continue
			}
			seq, err := e.journal.Append(req.cmd) // write-ahead: durable before applied
			if err != nil {
				req.reply <- submitResp{err: err}
				continue
			}
			events := e.apply(seq, req.cmd)
			if e.sink != nil && len(events) > 0 {
				e.sink(events)
			}
			req.reply <- submitResp{events: events}
		}
	}
}

// Submit sequences a command through the engine and returns the events it
// produced. Safe for concurrent use.
func (e *Engine) Submit(ctx context.Context, cmd Command) ([]Event, error) {
	req := submitReq{cmd: cmd, reply: make(chan submitResp, 1)}
	select {
	case e.cmdCh <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case resp := <-req.reply:
		return resp.events, resp.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Halt pauses acceptance of new orders for a symbol (control-plane action,
// e.g. during incidents). Resting orders and cancels are unaffected.
func (e *Engine) Halt(symbol string) {
	e.haltMu.Lock()
	e.halted[symbol] = true
	e.haltMu.Unlock()
}

// Resume re-enables order acceptance for a symbol.
func (e *Engine) Resume(symbol string) {
	e.haltMu.Lock()
	delete(e.halted, symbol)
	e.haltMu.Unlock()
}

// IsHalted reports whether new orders are paused for a symbol.
func (e *Engine) IsHalted(symbol string) bool {
	e.haltMu.RLock()
	defer e.haltMu.RUnlock()
	return e.halted[symbol]
}

// Depth exposes an order-book snapshot for market data. Safe to call
// concurrently with the engine loop (read-locked against apply).
func (e *Engine) Depth(symbol string, levels int) (bids, asks []orderbook.PriceLevel, err error) {
	e.booksMu.RLock()
	defer e.booksMu.RUnlock()
	ob, ok := e.books[symbol]
	if !ok {
		return nil, nil, ErrUnknownSymbol
	}
	bids, asks = ob.Depth(levels)
	return bids, asks, nil
}

// OpenOrders returns a user's resting orders across all symbols. Read-locked so
// it is safe to call from HTTP handlers while the engine processes commands.
func (e *Engine) OpenOrders(userID int64) []OpenOrder {
	e.booksMu.RLock()
	defer e.booksMu.RUnlock()
	out := []OpenOrder{}
	for id, owner := range e.orders {
		if owner.userID != userID {
			continue
		}
		ord, ok := e.books[owner.symbol].Order(id)
		if !ok {
			continue
		}
		out = append(out, OpenOrder{
			OrderID: id, Symbol: owner.symbol, Side: owner.side,
			Price: ord.Price, Quantity: ord.Quantity, Remaining: ord.Remaining,
		})
	}
	return out
}

// Symbols returns the trading pairs this engine serves (set at construction).
func (e *Engine) Symbols() []string {
	out := make([]string, 0, len(e.books))
	for s := range e.books {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// apply executes one command against the book, deterministically. It never
// returns an error: invalid commands become OrderRejected events so that the
// journal can always be replayed in full.
func (e *Engine) apply(seq uint64, cmd Command) []Event {
	// Single writer: serialize against concurrent Depth/OpenOrders readers.
	e.booksMu.Lock()
	defer e.booksMu.Unlock()

	ob, ok := e.books[cmd.Symbol]
	if !ok {
		return []Event{e.reject(seq, cmd, ErrUnknownSymbol.Error())}
	}
	switch cmd.Type {
	case CmdPlaceLimit, CmdPlaceMarket:
		var trades []orderbook.Trade
		var err error
		if cmd.Type == CmdPlaceLimit {
			trades, err = ob.PlaceLimit(cmd.OrderID, cmd.Side, cmd.Price, cmd.Qty)
		} else {
			trades, err = ob.PlaceMarket(cmd.OrderID, cmd.Side, cmd.Qty)
		}
		if err != nil {
			return []Event{e.reject(seq, cmd, err.Error())}
		}
		events := []Event{{
			Type: EvtOrderAccepted, Seq: seq, Index: 0, Symbol: cmd.Symbol,
			OrderID: cmd.OrderID, UserID: cmd.UserID, Side: cmd.Side,
			Price: cmd.Price, Qty: cmd.Qty,
		}}
		for _, tr := range trades {
			maker := e.orders[tr.MakerOrderID]
			events = append(events, Event{
				Type: EvtTrade, Seq: seq, Index: len(events), Symbol: cmd.Symbol,
				Side:  cmd.Side, // the taker's side: tells settlement who bought
				Price: tr.Price, Qty: tr.Quantity,
				MakerOrderID: tr.MakerOrderID, TakerOrderID: tr.TakerOrderID,
				MakerUserID: maker.userID, TakerUserID: cmd.UserID,
			})
			// A maker that was fully consumed is no longer resting; drop it so
			// `orders` stays exactly the set of live resting orders.
			if _, stillResting := ob.Order(tr.MakerOrderID); !stillResting {
				delete(e.orders, tr.MakerOrderID)
			}
		}
		// Record the taker only if it actually rested (a limit order with an
		// unfilled remainder). Fully-filled and market orders never rest.
		if _, resting := ob.Order(cmd.OrderID); resting {
			e.orders[cmd.OrderID] = ownedOrder{userID: cmd.UserID, side: cmd.Side, symbol: cmd.Symbol}
		}
		return events

	case CmdCancel:
		owner, live := e.orders[cmd.OrderID]
		// A cancel that names a user may only touch that user's order.
		if cmd.UserID != 0 && live && owner.userID != cmd.UserID {
			return []Event{e.reject(seq, cmd, "not order owner")}
		}
		cancelled, err := ob.Cancel(cmd.OrderID)
		if err != nil {
			return []Event{e.reject(seq, cmd, err.Error())}
		}
		delete(e.orders, cmd.OrderID)
		// Price and Qty report the unfilled remainder so the ledger can
		// release exactly the funds still reserved for this order.
		ev := Event{
			Type: EvtOrderCancelled, Seq: seq, Index: 0, Symbol: cmd.Symbol,
			OrderID: cmd.OrderID, Side: cancelled.Side,
			Price: cancelled.Price, Qty: cancelled.Remaining,
		}
		if live {
			ev.UserID = owner.userID
		}
		return []Event{ev}

	default:
		return []Event{e.reject(seq, cmd, "unknown command type")}
	}
}

func (e *Engine) reject(seq uint64, cmd Command, reason string) Event {
	return Event{
		Type: EvtOrderRejected, Seq: seq, Index: 0, Symbol: cmd.Symbol,
		OrderID: cmd.OrderID, UserID: cmd.UserID, Reason: reason,
	}
}

// MarshalCommand / UnmarshalCommand define the journal wire format (one JSON
// object per record), shared by all Journal implementations.
func MarshalCommand(cmd Command) ([]byte, error) { return json.Marshal(cmd) }

// UnmarshalCommand parses a journal record.
func UnmarshalCommand(b []byte) (Command, error) {
	var c Command
	err := json.Unmarshal(b, &c)
	return c, err
}
