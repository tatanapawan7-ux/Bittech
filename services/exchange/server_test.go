package exchange

// End-to-end test of the composed exchange over HTTP: signup -> login ->
// faucet deposit -> place crossing orders -> trade visible on the WebSocket
// feed, in balances, depth, and trade history. Uses the real ledger (Postgres)
// and engine; accounts use the in-memory store.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	accounthttp "github.com/tatanapawan7-ux/bittech/services/account-service/httpapi"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
)

func newE2EServer(t *testing.T) *httptest.Server {
	t.Helper()
	f := setup(t) // provisions Postgres ledger + running engine
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	accounts := account.NewService(account.NewMemStore())
	hub := NewHub()

	// Rebuild engine with hub publishing (setup wired no sink): simplest is a
	// second engine sharing nothing; instead, reuse f.trading's engine via a
	// wrapper is not possible — so compose a fresh stack here.
	eng, err := engine.New([]string{sym}, &engine.MemJournal{}, hub.Publish)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go eng.Run(ctx)
	trading := NewTrading(eng, f.ledger)

	accountAPI := accounthttp.New(accounts, log)
	mux := http.NewServeMux()
	for _, route := range []string{"/v1/signup", "/v1/login", "/v1/me", "/v1/2fa/", "/v1/apikeys"} {
		mux.Handle(route, accountAPI)
	}
	mux.Handle("/", NewServer(trading, eng, f.ledger, accounts, hub, log, true))
	return httptest.NewServer(mux)
}

func call(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func signupAndLogin(t *testing.T, base, email string) string {
	t.Helper()
	if code, body := call(t, "POST", base+"/v1/signup", "", map[string]string{"email": email, "password": "averysecurepw"}); code != 201 {
		t.Fatalf("signup %s: %d %v", email, code, body)
	}
	code, body := call(t, "POST", base+"/v1/login", "", map[string]string{"email": email, "password": "averysecurepw"})
	if code != 200 {
		t.Fatalf("login %s: %d %v", email, code, body)
	}
	return body["token"].(string)
}

func TestEndToEndTradeOverHTTP(t *testing.T) {
	ts := newE2EServer(t)
	defer ts.Close()

	// NOTE: the ledger fixture created users 1 and 2 in Postgres; the memstore
	// account service assigns the same ids 1 and 2, keeping them consistent.
	seller := signupAndLogin(t, ts.URL, "u1@t.co")
	buyer := signupAndLogin(t, ts.URL, "u2@t.co")

	// Fund via dev faucet.
	if code, b := call(t, "POST", ts.URL+"/v1/dev/deposit", seller, map[string]any{"asset": "BTC", "amount": 10}); code != 200 {
		t.Fatalf("faucet: %d %v", code, b)
	}
	if code, b := call(t, "POST", ts.URL+"/v1/dev/deposit", buyer, map[string]any{"asset": "USDT", "amount": 1000}); code != 200 {
		t.Fatalf("faucet: %d %v", code, b)
	}

	// Subscribe to the market feed before trading.
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/v1/ws?symbol=" + sym
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	// Seller asks 10 @ 50; buyer lifts it.
	if code, b := call(t, "POST", ts.URL+"/v1/orders", seller, map[string]any{
		"symbol": sym, "type": "limit", "side": "sell", "price": 50, "qty": 10,
	}); code != 201 {
		t.Fatalf("ask: %d %v", code, b)
	}
	code, body := call(t, "POST", ts.URL+"/v1/orders", buyer, map[string]any{
		"symbol": sym, "type": "limit", "side": "buy", "price": 50, "qty": 10,
	})
	if code != 201 {
		t.Fatalf("bid: %d %v", code, body)
	}

	// The feed must deliver the trade (the ask batch, then the bid+trade batch).
	sawTrade := false
	for !sawTrade {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("websocket read: %v", err)
		}
		var events []engine.Event
		if err := json.Unmarshal(msg, &events); err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			if ev.Type == engine.EvtTrade && ev.Price == 50 && ev.Qty == 10 {
				sawTrade = true
			}
		}
	}

	// Balances settled.
	code, body = call(t, "GET", ts.URL+"/v1/balances", buyer, nil)
	if code != 200 {
		t.Fatalf("balances: %d %v", code, body)
	}
	got := map[string]float64{}
	for _, raw := range body["balances"].([]any) {
		b := raw.(map[string]any)
		got[b["asset"].(string)+"/"+b["kind"].(string)] = b["balance"].(float64)
	}
	if got["BTC/main"] != 10 || got["USDT/main"] != 500 {
		t.Fatalf("buyer balances wrong: %v", got)
	}

	// Public market data reflects the trade.
	code, body = call(t, "GET", ts.URL+"/v1/trades?symbol="+sym, "", nil)
	if code != 200 || len(body["trades"].([]any)) != 1 {
		t.Fatalf("trades: %d %v", code, body)
	}
	code, body = call(t, "GET", ts.URL+"/v1/depth?symbol="+sym, "", nil)
	if code != 200 {
		t.Fatalf("depth: %d %v", code, body)
	}

	// Auth is enforced on trading routes.
	if code, _ := call(t, "POST", ts.URL+"/v1/orders", "", map[string]any{}); code != 401 {
		t.Fatalf("unauthenticated order placement: %d", code)
	}
}
