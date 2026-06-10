package orderbook

import "testing"

// helper: total quantity across a slice of trades.
func totalQty(trades []Trade) int64 {
	var t int64
	for _, tr := range trades {
		t += tr.Quantity
	}
	return t
}

func TestSimpleFullMatch(t *testing.T) {
	ob := New("BTC-USDT")
	if _, err := ob.PlaceLimit("ask1", Sell, 10, 100); err != nil {
		t.Fatal(err)
	}
	trades, err := ob.PlaceLimit("bid1", Buy, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	if trades[0].Price != 10 || trades[0].Quantity != 100 {
		t.Fatalf("unexpected trade: %+v", trades[0])
	}
	if trades[0].MakerOrderID != "ask1" || trades[0].TakerOrderID != "bid1" {
		t.Fatalf("maker/taker wrong: %+v", trades[0])
	}
	// Book must be empty now.
	if _, ok := ob.BestAsk(); ok {
		t.Fatal("expected no asks remaining")
	}
	if _, ok := ob.BestBid(); ok {
		t.Fatal("expected no bids remaining")
	}
}

func TestPartialFillRests(t *testing.T) {
	ob := New("BTC-USDT")
	ob.PlaceLimit("ask1", Sell, 10, 100)
	trades, _ := ob.PlaceLimit("bid1", Buy, 10, 60)
	if totalQty(trades) != 60 {
		t.Fatalf("expected 60 filled, got %d", totalQty(trades))
	}
	// 40 should remain on the ask side at price 10.
	if best, ok := ob.BestAsk(); !ok || best != 10 {
		t.Fatalf("expected ask remaining at 10, got %d ok=%v", best, ok)
	}
	if _, ok := ob.BestBid(); ok {
		t.Fatal("buy was fully filled; should not rest")
	}
}

func TestPriceTimePriority(t *testing.T) {
	ob := New("BTC-USDT")
	// Two asks at the same price; ask1 arrives first and must fill first.
	ob.PlaceLimit("ask1", Sell, 10, 50)
	ob.PlaceLimit("ask2", Sell, 10, 50)
	trades, _ := ob.PlaceLimit("bid1", Buy, 10, 60)
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}
	if trades[0].MakerOrderID != "ask1" || trades[0].Quantity != 50 {
		t.Fatalf("first fill should be ask1 x50, got %+v", trades[0])
	}
	if trades[1].MakerOrderID != "ask2" || trades[1].Quantity != 10 {
		t.Fatalf("second fill should be ask2 x10, got %+v", trades[1])
	}
}

func TestNoCrossRestsOnBook(t *testing.T) {
	ob := New("BTC-USDT")
	ob.PlaceLimit("ask1", Sell, 11, 100)
	trades, _ := ob.PlaceLimit("bid1", Buy, 10, 100) // below ask, no cross
	if len(trades) != 0 {
		t.Fatalf("expected no trades, got %d", len(trades))
	}
	if best, ok := ob.BestBid(); !ok || best != 10 {
		t.Fatalf("expected resting bid at 10, got %d ok=%v", best, ok)
	}
}

func TestMarketOrderSweepsLevels(t *testing.T) {
	ob := New("BTC-USDT")
	ob.PlaceLimit("ask1", Sell, 10, 50)
	ob.PlaceLimit("ask2", Sell, 11, 50)
	ob.PlaceLimit("ask3", Sell, 12, 50)
	trades, _ := ob.PlaceMarket("mkt1", Buy, 120)
	if totalQty(trades) != 120 {
		t.Fatalf("expected 120 filled, got %d", totalQty(trades))
	}
	// Best price level (10) should be consumed; lowest remaining ask is 12 with 30 left.
	if best, ok := ob.BestAsk(); !ok || best != 12 {
		t.Fatalf("expected best ask 12, got %d ok=%v", best, ok)
	}
}

func TestCancelRemovesResting(t *testing.T) {
	ob := New("BTC-USDT")
	ob.PlaceLimit("ask1", Sell, 10, 100)
	if err := ob.Cancel("ask1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := ob.BestAsk(); ok {
		t.Fatal("ask should be gone after cancel")
	}
	if err := ob.Cancel("ask1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRejectsInvalidAndDuplicate(t *testing.T) {
	ob := New("BTC-USDT")
	if _, err := ob.PlaceLimit("x", Buy, 0, 10); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid for zero price, got %v", err)
	}
	ob.PlaceLimit("dup", Buy, 10, 10)
	if _, err := ob.PlaceLimit("dup", Buy, 10, 10); err != ErrDuplicateID {
		t.Fatalf("expected ErrDuplicateID, got %v", err)
	}
}
