package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

// Server is the public trading API:
//
//	POST /v1/orders         (auth) {symbol, type:"limit"|"market", side:"buy"|"sell", price, qty}
//	POST /v1/orders/cancel  (auth) {symbol, order_id}
//	GET  /v1/balances       (auth)
//	GET  /v1/depth          ?symbol=BTC-USDT&levels=20
//	GET  /v1/trades         ?symbol=BTC-USDT
//	GET  /v1/ws             ?symbol=BTC-USDT   (WebSocket market event stream)
//	POST /v1/dev/deposit    (auth, only when faucet enabled) {asset, amount}
//
// Account endpoints (/v1/signup, /v1/login, ...) are mounted by the composing
// binary alongside this server.
type Server struct {
	trading  *Trading
	eng      *engine.Engine
	ledger   *ledger.Service
	accounts *account.Service
	hub      *Hub
	mux      *http.ServeMux
	log      *slog.Logger
	faucet   bool
}

// NewServer wires the trading API routes.
func NewServer(trading *Trading, eng *engine.Engine, led *ledger.Service, accounts *account.Service, hub *Hub, log *slog.Logger, faucet bool) *Server {
	s := &Server{
		trading: trading, eng: eng, ledger: led, accounts: accounts,
		hub: hub, mux: http.NewServeMux(), log: log, faucet: faucet,
	}
	s.mux.HandleFunc("POST /v1/orders", s.authed(s.handlePlaceOrder))
	s.mux.HandleFunc("POST /v1/orders/cancel", s.authed(s.handleCancelOrder))
	s.mux.HandleFunc("GET /v1/balances", s.authed(s.handleBalances))
	s.mux.HandleFunc("GET /v1/depth", s.handleDepth)
	s.mux.HandleFunc("GET /v1/trades", s.handleTrades)
	s.mux.HandleFunc("GET /v1/ws", s.handleWS)
	if faucet {
		s.mux.HandleFunc("POST /v1/dev/deposit", s.authed(s.handleDevDeposit))
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) authed(next func(http.ResponseWriter, *http.Request, *account.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		u, err := s.accounts.Authenticate(r.Context(), token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		next(w, r, u)
	}
}

func (s *Server) handlePlaceOrder(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct {
		Symbol string `json:"symbol"`
		Type   string `json:"type"`
		Side   string `json:"side"`
		Price  int64  `json:"price"`
		Qty    int64  `json:"qty"`
	}
	if !decode(w, r, &req) {
		return
	}
	side, ok := parseSide(req.Side)
	if !ok {
		writeErr(w, http.StatusBadRequest, "side must be buy or sell")
		return
	}
	events, err := s.trading.PlaceOrder(r.Context(), u.ID, req.Symbol, req.Type, side, req.Price, req.Qty)
	if err != nil {
		s.writeTradingErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"events": events})
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct {
		Symbol  string `json:"symbol"`
		OrderID string `json:"order_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	events, err := s.trading.CancelOrder(r.Context(), u.ID, req.Symbol, req.OrderID)
	if err != nil {
		s.writeTradingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleBalances(w http.ResponseWriter, r *http.Request, u *account.User) {
	balances, err := s.ledger.UserBalances(r.Context(), u.ID)
	if err != nil {
		s.log.Error("balances", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balances": balances})
}

func (s *Server) handleDepth(w http.ResponseWriter, r *http.Request) {
	levels, _ := strconv.Atoi(r.URL.Query().Get("levels"))
	if levels <= 0 || levels > 500 {
		levels = 20
	}
	bids, asks, err := s.eng.Depth(r.URL.Query().Get("symbol"), levels)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bids": bids, "asks": asks})
}

func (s *Server) handleTrades(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"trades": s.trading.RecentTrades(r.URL.Query().Get("symbol")),
	})
}

// handleWS streams a symbol's engine events (accepted/trade/cancelled) as JSON
// arrays — the live feed a trading UI builds its tape and book deltas from.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		writeErr(w, http.StatusBadRequest, "symbol required")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	events, cancel := s.hub.Subscribe(symbol)
	defer cancel()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-events:
			if !ok { // we lagged and were dropped: tell the client to resync
				conn.Close(websocket.StatusPolicyViolation, "subscriber lagged; resync from snapshot")
				return
			}
			b, err := json.Marshal(batch)
			if err != nil {
				return
			}
			wctx, done := context.WithTimeout(ctx, 5*time.Second)
			err = conn.Write(wctx, websocket.MessageText, b)
			done()
			if err != nil {
				return
			}
		}
	}
}

// handleDevDeposit credits play money so the flow can be exercised before the
// wallet/custody service exists. Compiled in only when the faucet flag is set.
func (s *Server) handleDevDeposit(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct {
		Asset  string `json:"asset"`
		Amount int64  `json:"amount"`
	}
	if !decode(w, r, &req) {
		return
	}
	key, err := newID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.ledger.Deposit(r.Context(), u.ID, req.Asset, req.Amount, "faucet-"+key); err != nil {
		s.writeTradingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) writeTradingErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrInsufficientFunds):
		writeErr(w, http.StatusUnprocessableEntity, "insufficient funds")
	case errors.Is(err, ErrRejected), errors.Is(err, ErrBadOrder),
		errors.Is(err, ErrUnsupported), errors.Is(err, ledger.ErrUnbalanced):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		s.log.Error("trading error", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func parseSide(s string) (orderbook.Side, bool) {
	switch s {
	case "buy":
		return orderbook.Buy, true
	case "sell":
		return orderbook.Sell, true
	}
	return 0, false
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
