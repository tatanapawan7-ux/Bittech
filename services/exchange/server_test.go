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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/account-service/auth"
	accounthttp "github.com/tatanapawan7-ux/bittech/services/account-service/httpapi"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
)

// e2e bundles the test server with the handles tests need to drive it.
type e2e struct {
	ts       *httptest.Server
	eng      *engine.Engine
	store    *account.MemStore
	accounts *account.Service
}

func newE2EServer(t *testing.T) *e2e {
	t.Helper()
	f := setup(t) // provisions Postgres ledger + running engine
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cipher, err := auth.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := account.NewMemStore()
	accounts := account.NewService(store).WithCipher(cipher)
	hub := NewHub()

	// Fresh engine wired to publish onto the hub.
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
	return &e2e{ts: httptest.NewServer(mux), eng: eng, store: store, accounts: accounts}
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
	env := newE2EServer(t)
	ts := env.ts
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

// TestAPIKeyOrderPlacement: a programmatic client places an order using an
// HMAC-signed API key instead of a session token.
func TestAPIKeyOrderPlacement(t *testing.T) {
	env := newE2EServer(t)
	ts := env.ts
	defer ts.Close()
	ctx := context.Background()

	token := signupAndLogin(t, ts.URL, "u1@t.co") // becomes user 1
	if code, b := call(t, "POST", ts.URL+"/v1/dev/deposit", token, map[string]any{"asset": "USDT", "amount": 1000}); code != 200 {
		t.Fatalf("faucet: %d %v", code, b)
	}
	// Mint an API key.
	code, body := call(t, "POST", ts.URL+"/v1/apikeys", token, map[string]string{"label": "bot"})
	if code != 201 {
		t.Fatalf("apikeys: %d %v", code, body)
	}
	keyID, secret := body["key_id"].(string), body["secret"].(string)

	// Sign and send an order with no session token, only API-key headers.
	orderBody := `{"symbol":"BTC-USDT","type":"limit","side":"buy","price":50,"qty":4}`
	now := time.Now()
	sig := auth.SignRequest(secret, now, "POST", "/v1/orders", orderBody)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/orders", strings.NewReader(orderBody))
	req.Header.Set("X-API-Key", keyID)
	req.Header.Set("X-API-Timestamp", strconv.FormatInt(now.UnixMilli(), 10))
	req.Header.Set("X-API-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("api-key order: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A bad signature is rejected.
	req2, _ := http.NewRequest("POST", ts.URL+"/v1/orders", strings.NewReader(orderBody))
	req2.Header.Set("X-API-Key", keyID)
	req2.Header.Set("X-API-Timestamp", strconv.FormatInt(now.UnixMilli(), 10))
	req2.Header.Set("X-API-Signature", "deadbeef")
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2.StatusCode != 401 {
		t.Fatalf("bad signature: status %d", resp2.StatusCode)
	}
	resp2.Body.Close()
	_ = ctx
}

// TestAdminHaltAndReconcile: admin-only endpoints gate on the role, halt blocks
// trading, and reconcile reports a healthy ledger.
func TestAdminHaltAndReconcile(t *testing.T) {
	env := newE2EServer(t)
	ts := env.ts
	defer ts.Close()

	userTok := signupAndLogin(t, ts.URL, "u1@t.co")  // user 1, non-admin
	adminTok := signupAndLogin(t, ts.URL, "u2@t.co") // user 2
	if err := env.store.SetAdmin(2); err != nil {    // promote user 2
		t.Fatal(err)
	}

	// Non-admin is forbidden.
	if code, _ := call(t, "GET", ts.URL+"/v1/admin/reconcile", userTok, nil); code != 403 {
		t.Fatalf("non-admin reconcile: %d", code)
	}
	// Admin reconcile reports healthy.
	code, body := call(t, "GET", ts.URL+"/v1/admin/reconcile", adminTok, nil)
	if code != 200 || body["ok"] != true {
		t.Fatalf("reconcile: %d %v", code, body)
	}

	// Halt the symbol, then orders are rejected.
	if code, _ := call(t, "POST", ts.URL+"/v1/admin/halt", adminTok, map[string]string{"symbol": sym}); code != 200 {
		t.Fatalf("halt: %d", code)
	}
	call(t, "POST", ts.URL+"/v1/dev/deposit", userTok, map[string]any{"asset": "USDT", "amount": 1000})
	code, body = call(t, "POST", ts.URL+"/v1/orders", userTok, map[string]any{
		"symbol": sym, "type": "limit", "side": "buy", "price": 50, "qty": 1,
	})
	if code != 400 {
		t.Fatalf("order during halt should be rejected: %d %v", code, body)
	}

	// Resume restores trading.
	call(t, "POST", ts.URL+"/v1/admin/resume", adminTok, map[string]string{"symbol": sym})
	if code, _ := call(t, "POST", ts.URL+"/v1/orders", userTok, map[string]any{
		"symbol": sym, "type": "limit", "side": "buy", "price": 50, "qty": 1,
	}); code != 201 {
		t.Fatalf("order after resume: %d", code)
	}
}
