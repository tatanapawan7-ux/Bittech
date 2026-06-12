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
//	REDIS_ADDR    Redis address for distributed rate limiting (default in-memory).
//	APIKEY_ENC_KEY 64 hex chars (32 bytes) to enable API-key signing; required for /v1/apikeys.
//	RATE_LIMIT     Sustained requests/sec per client (default 20, burst 2x).
package main

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/tatanapawan7-ux/bittech/libs/httpx"
	"github.com/tatanapawan7-ux/bittech/libs/ratelimit"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/account-service/auth"
	accounthttp "github.com/tatanapawan7-ux/bittech/services/account-service/httpapi"
	"github.com/tatanapawan7-ux/bittech/services/exchange"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/custody"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/wallet"
	wallethttp "github.com/tatanapawan7-ux/bittech/services/wallet-service/wallethttp"
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
	if hexKey := os.Getenv("APIKEY_ENC_KEY"); hexKey != "" {
		key, err := hex.DecodeString(hexKey)
		if err != nil {
			log.Error("APIKEY_ENC_KEY must be hex", "err", err)
			os.Exit(1)
		}
		cipher, err := auth.NewCipher(key)
		if err != nil {
			log.Error("APIKEY_ENC_KEY invalid", "err", err)
			os.Exit(1)
		}
		accounts.WithCipher(cipher)
		log.Info("API-key signing enabled")
	}
	led := ledger.New(pool)
	trading := exchange.NewTrading(eng, led)
	faucet := os.Getenv("DEV_FAUCET") == "1"
	if faucet {
		log.Warn("dev faucet ENABLED — play-money deposits are open")
	}

	// Wallet/custody: the mock provider stands in until a real custody vendor
	// (Fireblocks/BitGo) is configured — swapping it is a one-line change here.
	withdrawLimit := int64(100_000_000_000)
	wal := wallet.New(pool, custody.NewMock(), led, withdrawLimit)
	webhookSecret := envOr("CUSTODY_WEBHOOK_SECRET", "dev-webhook-secret")

	// One mux: account + trading + wallet routes.
	accountAPI := accounthttp.New(accounts, log)
	mux := http.NewServeMux()
	for _, route := range []string{"/v1/signup", "/v1/login", "/v1/me", "/v1/2fa/", "/v1/apikeys"} {
		mux.Handle(route, accountAPI)
	}
	walletAPI := wallethttp.New(wal, accounts, log, webhookSecret)
	for _, route := range []string{"/v1/wallet/", "/v1/admin/"} {
		mux.Handle(route, walletAPI)
	}
	mux.Handle("/", exchange.NewServer(trading, eng, led, accounts, hub, log, faucet))

	// Observability + protection at the edge.
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector())
	metrics := httpx.NewMetrics(reg)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// Rate limit per client. Redis makes the limit global across instances;
	// without REDIS_ADDR it falls back to an in-process limiter.
	rate := 20.0
	if v := os.Getenv("RATE_LIMIT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			rate = f
		}
	}
	cfg := ratelimit.Config{Rate: rate, Burst: rate * 2}
	var limiter ratelimit.Limiter
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		limiter = ratelimit.NewRedis(redis.NewClient(&redis.Options{Addr: addr}), cfg, "rl")
		log.Info("rate limiting via redis", "addr", addr, "rate", rate)
	} else {
		limiter = ratelimit.NewMemory(cfg)
		log.Info("rate limiting in-memory", "rate", rate)
	}

	// /metrics is exempt from rate limiting so scrapers are never throttled.
	limited := httpx.RateLimit(limiter, httpx.ClientIP, metrics.MarkLimited())(mux)
	handler := metrics.Instrument(limited)

	addr := envOr("LISTEN_ADDR", ":8080")
	log.Info("exchange listening", "addr", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
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
