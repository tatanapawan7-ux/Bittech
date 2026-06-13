// Package orderbook implements an in-memory limit order book with strict
// price-time priority — the core of the matching engine.
//
// Design notes (mirrors how production exchanges build this):
//   - The book is a pure, in-memory, deterministic state machine. It performs no
//     I/O and holds no locks; a single goroutine owns one book (or shard of
//     symbols) and feeds it a totally ordered stream of commands. Determinism is
//     what lets the engine be rebuilt by replaying its input log after a crash.
//   - Prices and quantities are integers in the asset's smallest unit. No floats.
//   - Each price level is a FIFO queue, so orders at the same price are matched in
//     arrival order (time priority). Cancellation is O(1) via a stored list element.
package orderbook

import (
	"container/list"
	"errors"
	"sort"
)

// Side identifies which side of the book an order sits on.
type Side int

const (
	// Buy is a bid (willingness to buy the base asset).
	Buy Side = iota
	// Sell is an ask (willingness to sell the base asset).
	Sell
)

func (s Side) String() string {
	if s == Buy {
		return "buy"
	}
	return "sell"
}

var (
	// ErrDuplicateID is returned when placing an order whose ID is already live.
	ErrDuplicateID = errors.New("orderbook: duplicate order id")
	// ErrNotFound is returned when cancelling an unknown order.
	ErrNotFound = errors.New("orderbook: order not found")
	// ErrInvalid is returned for malformed orders (non-positive price/quantity).
	ErrInvalid = errors.New("orderbook: invalid order")
)

// Order is a resting or incoming order. Price is in quote units per base unit;
// for market orders Price is ignored. Remaining tracks the unfilled quantity.
type Order struct {
	ID        string
	Side      Side
	Price     int64
	Quantity  int64
	Remaining int64
	Seq       int64 // assigned by the book; establishes time priority

	elem *list.Element // position in its price level (nil until resting)
	side *bookSide     // owning side (nil until resting)
}

// Trade is emitted when an incoming (taker) order matches a resting (maker) one.
// Trades always execute at the resting maker's price.
type Trade struct {
	TakerOrderID string
	MakerOrderID string
	Price        int64
	Quantity     int64
}

// OrderBook holds the bids and asks for a single trading symbol.
type OrderBook struct {
	Symbol string
	bids   *bookSide // descending: best (highest) bid last in price slice
	asks   *bookSide // ascending: best (lowest) ask first in price slice
	live   map[string]*Order
	seq    int64
}

// New creates an empty order book for the given symbol (e.g. "BTC-USDT").
func New(symbol string) *OrderBook {
	return &OrderBook{
		Symbol: symbol,
		bids:   newBookSide(Buy),
		asks:   newBookSide(Sell),
		live:   make(map[string]*Order),
	}
}

// PlaceLimit submits a limit order. It matches against the opposite side at or
// better than price; any unfilled remainder rests on the book.
func (ob *OrderBook) PlaceLimit(id string, side Side, price, qty int64) ([]Trade, error) {
	if price <= 0 || qty <= 0 {
		return nil, ErrInvalid
	}
	if _, ok := ob.live[id]; ok {
		return nil, ErrDuplicateID
	}
	ob.seq++
	o := &Order{ID: id, Side: side, Price: price, Quantity: qty, Remaining: qty, Seq: ob.seq}
	trades := ob.match(o)
	if o.Remaining > 0 {
		ob.rest(o)
	}
	return trades, nil
}

// PlaceMarket submits a market order. It matches against the best available
// prices until filled or the book is exhausted; any unfilled remainder is
// discarded (a market order never rests).
func (ob *OrderBook) PlaceMarket(id string, side Side, qty int64) ([]Trade, error) {
	if qty <= 0 {
		return nil, ErrInvalid
	}
	if _, ok := ob.live[id]; ok {
		return nil, ErrDuplicateID
	}
	ob.seq++
	o := &Order{ID: id, Side: side, Price: 0, Quantity: qty, Remaining: qty, Seq: ob.seq}
	return ob.match(o), nil
}

// Cancel removes a resting order from the book and returns a copy of it (so
// callers can release the funds still reserved for the unfilled remainder).
func (ob *OrderBook) Cancel(id string) (Order, error) {
	o, ok := ob.live[id]
	if !ok {
		return Order{}, ErrNotFound
	}
	o.side.remove(o)
	delete(ob.live, id)
	cp := *o
	cp.elem, cp.side = nil, nil
	return cp, nil
}

// BestBid returns the highest bid price and true, or false if there are no bids.
func (ob *OrderBook) BestBid() (int64, bool) { return ob.bids.best() }

// BestAsk returns the lowest ask price and true, or false if there are no asks.
func (ob *OrderBook) BestAsk() (int64, bool) { return ob.asks.best() }

// Order returns a copy of a resting order by id, or false if it is not on the
// book (never placed, fully filled, or cancelled).
func (ob *OrderBook) Order(id string) (Order, bool) {
	o, ok := ob.live[id]
	if !ok {
		return Order{}, false
	}
	cp := *o
	cp.elem, cp.side = nil, nil
	return cp, true
}

// match crosses the incoming order against the opposite side, emitting trades.
func (ob *OrderBook) match(o *Order) []Trade {
	opp := ob.asks
	if o.Side == Sell {
		opp = ob.bids
	}
	var trades []Trade
	for o.Remaining > 0 {
		best, ok := opp.best()
		if !ok || !crosses(o, best) {
			break
		}
		queue := opp.levels[best]
		for o.Remaining > 0 && queue.Len() > 0 {
			front := queue.Front()
			maker := front.Value.(*Order)
			qty := min64(o.Remaining, maker.Remaining)
			trades = append(trades, Trade{
				TakerOrderID: o.ID,
				MakerOrderID: maker.ID,
				Price:        best,
				Quantity:     qty,
			})
			o.Remaining -= qty
			maker.Remaining -= qty
			if maker.Remaining == 0 {
				opp.remove(maker)
				delete(ob.live, maker.ID)
			}
		}
	}
	return trades
}

// crosses reports whether a taker order can trade against a resting price.
func crosses(o *Order, restingPrice int64) bool {
	if o.Price == 0 { // market order
		return true
	}
	if o.Side == Buy {
		return o.Price >= restingPrice // willing to pay at least the ask
	}
	return o.Price <= restingPrice // willing to accept at most the bid
}

// rest places the remaining quantity of an order onto its side of the book.
func (ob *OrderBook) rest(o *Order) {
	side := ob.bids
	if o.Side == Sell {
		side = ob.asks
	}
	side.add(o)
	ob.live[o.ID] = o
}

// bookSide holds one side of the book: price levels with FIFO queues, plus a
// sorted slice of prices for O(log n) best-price lookup.
type bookSide struct {
	side   Side
	levels map[int64]*list.List
	prices []int64 // always sorted ascending
}

func newBookSide(s Side) *bookSide {
	return &bookSide{side: s, levels: make(map[int64]*list.List)}
}

// best returns the best price on this side: the lowest ask or the highest bid.
func (b *bookSide) best() (int64, bool) {
	if len(b.prices) == 0 {
		return 0, false
	}
	if b.side == Sell {
		return b.prices[0], true
	}
	return b.prices[len(b.prices)-1], true
}

func (b *bookSide) add(o *Order) {
	queue, ok := b.levels[o.Price]
	if !ok {
		queue = list.New()
		b.levels[o.Price] = queue
		b.insertPrice(o.Price)
	}
	o.elem = queue.PushBack(o)
	o.side = b
}

func (b *bookSide) remove(o *Order) {
	queue := b.levels[o.Price]
	queue.Remove(o.elem)
	o.elem = nil
	if queue.Len() == 0 {
		delete(b.levels, o.Price)
		b.removePrice(o.Price)
	}
}

func (b *bookSide) insertPrice(p int64) {
	i := sort.Search(len(b.prices), func(i int) bool { return b.prices[i] >= p })
	b.prices = append(b.prices, 0)
	copy(b.prices[i+1:], b.prices[i:])
	b.prices[i] = p
}

func (b *bookSide) removePrice(p int64) {
	i := sort.Search(len(b.prices), func(i int) bool { return b.prices[i] >= p })
	if i < len(b.prices) && b.prices[i] == p {
		b.prices = append(b.prices[:i], b.prices[i+1:]...)
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
