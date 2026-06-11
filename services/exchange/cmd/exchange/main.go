// Command exchange boots the whole MVP backend in one process: account
// service, ledger, matching engine (file journal), and the trading API with
// its WebSocket market feed. One binary, one Postgres — the development and
// single-node deployment shape. The packages it composes are the same ones
// that deploy as separate services later.
//
// Configuration via environment:
//
//	DATABASE_URL  Postgres connection string (required).
//	SYMBOLS       Comma-separated pairs (default "BTC-USDT,ETH-USDT").
//	JOURNAL_DIR   Engine journal directory (default "./data").
//	LISTEN_ADDR   HTTP listen address (default ":8080").
//	DEV_FAUCET    "1" enables POST /v1/dev/deposit (play money; never in prod).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	accounthttp "github.com/tatanapawan7-ux/bittech/services/account-service/httpapi"
	"github.com/tatanapawan7-ux/bittech/services/exchange"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Error("connect postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Engine with durable journal.
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

	hub := exchange.NewHub()
	symbols := strings.Split(envOr("SYMBOLS", "BTC-USDT,ETH-USDT"), ",")
	eng, err := engine.New(symbols, journal, hub.Publish)
	if err != nil {
		log.Error("recover engine", "err", err)
		os.Exit(1)
	}
	go eng.Run(context.Background())
	log.Info("engine recovered", "symbols", symbols)

	// Services.
	accounts := account.NewService(account.NewPGStore(pool))
	led := ledger.New(pool)
	trading := exchange.NewTrading(eng, led)
	faucet := os.Getenv("DEV_FAUCET") == "1"
	if faucet {
		log.Warn("dev faucet ENABLED — play-money deposits are open")
	}

	// One mux: account routes + trading routes.
	accountAPI := accounthttp.New(accounts, log)
	mux := http.NewServeMux()
	for _, route := range []string{"/v1/signup", "/v1/login", "/v1/me", "/v1/2fa/", "/v1/apikeys"} {
		mux.Handle(route, accountAPI)
	}
	mux.Handle("/", exchange.NewServer(trading, eng, led, accounts, hub, log, faucet))

	addr := envOr("LISTEN_ADDR", ":8080")
	log.Info("exchange listening", "addr", addr)
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
