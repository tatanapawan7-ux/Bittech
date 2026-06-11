// Command matching-engine runs the matching engine with a file journal and a
// development HTTP API. In the target architecture commands arrive via the
// order gateway through Redpanda; this binary lets the engine be exercised
// standalone until that wiring lands (Phase 4).
//
// Configuration via environment:
//
//	SYMBOLS      Comma-separated trading pairs (default "BTC-USDT,ETH-USDT").
//	JOURNAL_DIR  Directory for journal files (default "./data").
//	LISTEN_ADDR  HTTP listen address (default ":8082").
//
// Endpoints:
//
//	POST /v1/orders  {type:"limit"|"market", symbol, order_id, user_id, side:"buy"|"sell", price, qty}
//	POST /v1/cancel  {symbol, order_id}
//	GET  /v1/depth?symbol=BTC-USDT&levels=20
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/orderbook"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	symbols := strings.Split(envOr("SYMBOLS", "BTC-USDT,ETH-USDT"), ",")
	dir := envOr("JOURNAL_DIR", "./data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Error("create journal dir", "err", err)
		os.Exit(1)
	}
	journal, err := engine.OpenFileJournal(filepath.Join(dir, "commands.journal"))
	if err != nil {
		log.Error("open journal", "err", err)
		os.Exit(1)
	}
	defer journal.Close()

	sink := func(events []engine.Event) {
		for _, ev := range events {
			log.Info("event", "type", ev.Type, "seq", ev.Seq, "symbol", ev.Symbol,
				"order_id", ev.OrderID, "price", ev.Price, "qty", ev.Qty)
		}
	}
	eng, err := engine.New(symbols, journal, sink)
	if err != nil {
		log.Error("recover engine", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	go eng.Run(ctx)
	log.Info("engine recovered", "symbols", symbols)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orders", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Type    string `json:"type"`
			Symbol  string `json:"symbol"`
			OrderID string `json:"order_id"`
			UserID  int64  `json:"user_id"`
			Side    string `json:"side"`
			Price   int64  `json:"price"`
			Qty     int64  `json:"qty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, http.StatusBadRequest, "malformed JSON")
			return
		}
		cmd := engine.Command{
			Symbol: req.Symbol, OrderID: req.OrderID, UserID: req.UserID,
			Price: req.Price, Qty: req.Qty,
		}
		switch req.Type {
		case "limit":
			cmd.Type = engine.CmdPlaceLimit
		case "market":
			cmd.Type = engine.CmdPlaceMarket
		default:
			httpErr(w, http.StatusBadRequest, "type must be limit or market")
			return
		}
		switch req.Side {
		case "buy":
			cmd.Side = orderbook.Buy
		case "sell":
			cmd.Side = orderbook.Sell
		default:
			httpErr(w, http.StatusBadRequest, "side must be buy or sell")
			return
		}
		events, err := eng.Submit(r.Context(), cmd)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	})
	mux.HandleFunc("POST /v1/cancel", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Symbol  string `json:"symbol"`
			OrderID string `json:"order_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, http.StatusBadRequest, "malformed JSON")
			return
		}
		events, err := eng.Submit(r.Context(), engine.Command{
			Type: engine.CmdCancel, Symbol: req.Symbol, OrderID: req.OrderID,
		})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	})
	mux.HandleFunc("GET /v1/depth", func(w http.ResponseWriter, r *http.Request) {
		levels, _ := strconv.Atoi(r.URL.Query().Get("levels"))
		if levels <= 0 {
			levels = 20
		}
		bids, asks, err := eng.Depth(r.URL.Query().Get("symbol"), levels)
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"bids": bids, "asks": asks})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := envOr("LISTEN_ADDR", ":8082")
	log.Info("matching-engine listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
