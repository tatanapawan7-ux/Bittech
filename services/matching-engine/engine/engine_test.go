package engine

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

const sym = "BTC-USDT"

// run starts an engine loop and returns the engine plus a stop func.
func run(t *testing.T, j Journal, sink Sink) (*Engine, context.CancelFunc) {
	t.Helper()
	e, err := New([]string{sym}, j, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	return e, cancel
}

func TestSubmitProducesAttributedEvents(t *testing.T) {
	e, stop := run(t, &MemJournal{}, nil)
	defer stop()
	ctx := context.Background()

	// User 1 posts an ask; user 2 lifts it.
	evs, err := e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "a1", UserID: 1, Side: orderbook.Sell, Price: 100, Qty: 5})
	if err != nil || len(evs) != 1 || evs[0].Type != EvtOrderAccepted {
		t.Fatalf("ask: %v %+v", err, evs)
	}
	evs, err = e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "b1", UserID: 2, Side: orderbook.Buy, Price: 100, Qty: 5})
	if err != nil || len(evs) != 2 {
		t.Fatalf("bid: %v %+v", err, evs)
	}
	tr := evs[1]
	if tr.Type != EvtTrade || tr.MakerUserID != 1 || tr.TakerUserID != 2 || tr.Price != 100 || tr.Qty != 5 {
		t.Fatalf("trade attribution wrong: %+v", tr)
	}
	// (Seq, Index) must be set for ledger idempotency.
	if tr.Seq == 0 {
		t.Fatal("trade missing journal seq")
	}
}

func TestInvalidCommandsBecomeRejections(t *testing.T) {
	e, stop := run(t, &MemJournal{}, nil)
	defer stop()
	ctx := context.Background()

	evs, err := e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: "NOPE-USD", OrderID: "x", Side: orderbook.Buy, Price: 1, Qty: 1})
	if err != nil || evs[0].Type != EvtOrderRejected {
		t.Fatalf("unknown symbol: %v %+v", err, evs)
	}
	evs, err = e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "x", Side: orderbook.Buy, Price: 0, Qty: 1})
	if err != nil || evs[0].Type != EvtOrderRejected {
		t.Fatalf("zero price: %v %+v", err, evs)
	}
	evs, err = e.Submit(ctx, Command{Type: CmdCancel, Symbol: sym, OrderID: "ghost"})
	if err != nil || evs[0].Type != EvtOrderRejected {
		t.Fatalf("cancel unknown: %v %+v", err, evs)
	}
}

// randomCommands generates a reproducible stream of mixed commands.
func randomCommands(n int, seed int64) []Command {
	rng := rand.New(rand.NewSource(seed))
	cmds := make([]Command, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("o%d", i)
		switch rng.Intn(10) {
		case 0, 1: // occasional cancel of a random earlier order
			cmds = append(cmds, Command{Type: CmdCancel, Symbol: sym, OrderID: fmt.Sprintf("o%d", rng.Intn(i+1))})
		case 2: // market order
			cmds = append(cmds, Command{
				Type: CmdPlaceMarket, Symbol: sym, OrderID: id, UserID: int64(rng.Intn(5) + 1),
				Side: orderbook.Side(rng.Intn(2)), Qty: int64(rng.Intn(20) + 1),
			})
		default: // limit order around a 100 +/- 10 mid
			cmds = append(cmds, Command{
				Type: CmdPlaceLimit, Symbol: sym, OrderID: id, UserID: int64(rng.Intn(5) + 1),
				Side: orderbook.Side(rng.Intn(2)), Price: int64(90 + rng.Intn(21)), Qty: int64(rng.Intn(20) + 1),
			})
		}
	}
	return cmds
}

// TestDeterministicReplay is the core event-sourcing guarantee: rebuilding the
// engine from its journal reproduces the exact same event stream and book state.
func TestDeterministicReplay(t *testing.T) {
	j := &MemJournal{}
	var liveEvents []Event
	e, stop := run(t, j, func(evs []Event) { liveEvents = append(liveEvents, evs...) })
	ctx := context.Background()

	for _, cmd := range randomCommands(500, 42) {
		if _, err := e.Submit(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	liveBids, liveAsks, _ := e.Depth(sym, 0)
	stop()
	if len(liveEvents) == 0 {
		t.Fatal("sink received no events")
	}

	// Rebuild a fresh engine from the same journal: book state must be identical.
	e2, err := New([]string{sym}, j, nil)
	if err != nil {
		t.Fatal(err)
	}
	reBids, reAsks, _ := e2.Depth(sym, 0)
	if !reflect.DeepEqual(liveBids, reBids) || !reflect.DeepEqual(liveAsks, reAsks) {
		t.Fatalf("book state diverged after replay:\nlive bids %v asks %v\nreplay bids %v asks %v",
			liveBids, liveAsks, reBids, reAsks)
	}

	// And replaying the journal through a second engine's apply path must
	// re-derive the exact same event stream (event-stream determinism).
	e3 := &Engine{books: map[string]*orderbook.OrderBook{sym: orderbook.New(sym)}, orders: make(map[string]ownedOrder)}
	var replayed []Event
	if err := j.Replay(func(seq uint64, cmd Command) error {
		replayed = append(replayed, e3.apply(seq, cmd)...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(liveEvents, replayed) {
		t.Fatalf("event streams diverged: live %d events, replayed %d", len(liveEvents), len(replayed))
	}
}

// TestFileJournalCrashRecovery simulates a crash: commands are journaled to
// disk, the engine is discarded, and a new engine recovers identical state.
func TestFileJournalCrashRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "btc-usdt.journal")
	j, err := OpenFileJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	e, stop := run(t, j, nil)
	ctx := context.Background()
	for _, cmd := range randomCommands(200, 7) {
		if _, err := e.Submit(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	wantBids, wantAsks, _ := e.Depth(sym, 0)
	stop()
	j.Close() // "crash"

	j2, err := OpenFileJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	e2, err := New([]string{sym}, j2, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotBids, gotAsks, _ := e2.Depth(sym, 0)
	if !reflect.DeepEqual(wantBids, gotBids) || !reflect.DeepEqual(wantAsks, gotAsks) {
		t.Fatalf("state lost across crash:\nwant bids %v asks %v\ngot bids %v asks %v",
			wantBids, wantAsks, gotBids, gotAsks)
	}

	// New commands resume the sequence after the recovered records.
	evs, err := e2Submit(t, e2, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "post-crash", UserID: 9, Side: orderbook.Buy, Price: 1, Qty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].Seq != 201 {
		t.Fatalf("sequence did not resume: got %d, want 201", evs[0].Seq)
	}
}

func e2Submit(t *testing.T, e *Engine, cmd Command) ([]Event, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	return e.Submit(ctx, cmd)
}

func TestDepthSnapshot(t *testing.T) {
	e, stop := run(t, &MemJournal{}, nil)
	defer stop()
	ctx := context.Background()
	e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "b1", Side: orderbook.Buy, Price: 99, Qty: 3})
	e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "b2", Side: orderbook.Buy, Price: 98, Qty: 2})
	e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "b3", Side: orderbook.Buy, Price: 99, Qty: 1})
	e.Submit(ctx, Command{Type: CmdPlaceLimit, Symbol: sym, OrderID: "a1", Side: orderbook.Sell, Price: 101, Qty: 4})

	bids, asks, err := e.Depth(sym, 10)
	if err != nil {
		t.Fatal(err)
	}
	wantBids := []orderbook.PriceLevel{{Price: 99, Quantity: 4}, {Price: 98, Quantity: 2}}
	wantAsks := []orderbook.PriceLevel{{Price: 101, Quantity: 4}}
	if !reflect.DeepEqual(bids, wantBids) || !reflect.DeepEqual(asks, wantAsks) {
		t.Fatalf("depth: bids %v asks %v", bids, asks)
	}
}
