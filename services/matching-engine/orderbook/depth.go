package orderbook

// PriceLevel is one row of an order-book depth snapshot: a price and the total
// resting quantity at that price.
type PriceLevel struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
}

// Depth returns up to maxLevels aggregated price levels per side, bids ordered
// best (highest) first and asks ordered best (lowest) first — the shape served
// to clients as the order-book snapshot.
func (ob *OrderBook) Depth(maxLevels int) (bids, asks []PriceLevel) {
	return ob.bids.depth(maxLevels), ob.asks.depth(maxLevels)
}

func (b *bookSide) depth(maxLevels int) []PriceLevel {
	n := len(b.prices)
	if maxLevels > 0 && maxLevels < n {
		n = maxLevels
	}
	out := make([]PriceLevel, 0, n)
	for i := 0; i < n; i++ {
		// prices is ascending; bids serve best-first from the top end.
		p := b.prices[i]
		if b.side == Buy {
			p = b.prices[len(b.prices)-1-i]
		}
		var qty int64
		for e := b.levels[p].Front(); e != nil; e = e.Next() {
			qty += e.Value.(*Order).Remaining
		}
		out = append(out, PriceLevel{Price: p, Quantity: qty})
	}
	return out
}
