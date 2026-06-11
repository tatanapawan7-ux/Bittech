package exchange

import (
	"sync"

	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
)

// Hub fans engine events out to market-data subscribers (the WebSocket feed).
// It is the in-process stand-in for the market-data service's pub/sub: the
// engine's sink publishes here, subscribers hold a buffered channel each.
// Slow subscribers are disconnected rather than allowed to stall the feed —
// clients are expected to resynchronize from a depth snapshot, which is the
// standard exchange-feed contract (snapshot + deltas, drop on lag).
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan []engine.Event]struct{} // symbol -> subscribers
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan []engine.Event]struct{})}
}

// Publish delivers an event batch to every subscriber of its symbol. It never
// blocks: subscribers that can't keep up are closed and removed.
func (h *Hub) Publish(events []engine.Event) {
	if len(events) == 0 {
		return
	}
	symbol := events[0].Symbol
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[symbol] {
		select {
		case ch <- events:
		default: // subscriber lagging: cut it loose
			close(ch)
			delete(h.subs[symbol], ch)
		}
	}
}

// Subscribe registers for a symbol's events. Call the returned cancel func to
// unsubscribe. The channel is closed on cancel or when the subscriber lags.
func (h *Hub) Subscribe(symbol string) (<-chan []engine.Event, func()) {
	ch := make(chan []engine.Event, 64)
	h.mu.Lock()
	if h.subs[symbol] == nil {
		h.subs[symbol] = make(map[chan []engine.Event]struct{})
	}
	h.subs[symbol][ch] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[symbol][ch]; ok {
			close(ch)
			delete(h.subs[symbol], ch)
		}
	}
	return ch, cancel
}
