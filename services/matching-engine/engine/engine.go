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
	books   map[string]*orderbook.OrderBook
	orders  map[string]ownedOrder // live order id -> attribution
	journal Journal
	sink    Sink
	cmdCh   chan submitReq
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

// Depth exposes an order-book snapshot for market data. It must only be called
// from the engine goroutine's context in production; the dev HTTP server calls
// it via Submit-free read access, which is safe only because the dev server
// serializes externally. (Phase 4 replaces this with a read-model.)
func (e *Engine) Depth(symbol string, levels int) (bids, asks []orderbook.PriceLevel, err error) {
	ob, ok := e.books[symbol]
	if !ok {
		return nil, nil, ErrUnknownSymbol
	}
	bids, asks = ob.Depth(levels)
	return bids, asks, nil
}

// apply executes one command against the book, deterministically. It never
// returns an error: invalid commands become OrderRejected events so that the
// journal can always be replayed in full.
func (e *Engine) apply(seq uint64, cmd Command) []Event {
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
		e.orders[cmd.OrderID] = ownedOrder{userID: cmd.UserID, side: cmd.Side, symbol: cmd.Symbol}
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
		}
		return events

	case CmdCancel:
		owner, live := e.orders[cmd.OrderID]
		if err := ob.Cancel(cmd.OrderID); err != nil {
			return []Event{e.reject(seq, cmd, err.Error())}
		}
		delete(e.orders, cmd.OrderID)
		ev := Event{
			Type: EvtOrderCancelled, Seq: seq, Index: 0, Symbol: cmd.Symbol,
			OrderID: cmd.OrderID,
		}
		if live {
			ev.UserID, ev.Side = owner.userID, owner.side
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
