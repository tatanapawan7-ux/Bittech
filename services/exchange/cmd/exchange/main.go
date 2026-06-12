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
//	BOOTSTRAP_ADMIN_EMAIL  If set, promotes this existing user to admin on boot.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/tatanapawan7-ux/bittech/libs/httpx"
	"github.com/tatanapawan7-ux/bittech/libs/ratelimit"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/account-service/auth"
	"github.com/tatanapawan7-ux/bittech/services/exchange"
	"github.com/tatanapawan7-ux/bittech/services/exchange/app"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/custody"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/wallet"
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

	// Promote a bootstrap admin so the operator endpoints are reachable on a
	// fresh deployment (the user must already exist via signup).
	if email := os.Getenv("BOOTSTRAP_ADMIN_EMAIL"); email != "" {
		tag, err := pool.Exec(context.Background(),
			`UPDATE users SET is_admin = true WHERE email = $1`, strings.ToLower(email))
		if err != nil {
			log.Error("bootstrap admin", "err", err)
		} else if tag.RowsAffected() == 0 {
			log.Warn("bootstrap admin: no such user (sign up first)", "email", email)
		} else {
			log.Info("bootstrap admin promoted", "email", email)
		}
	}

	// The engine and its goroutine are tied to a cancelable context so shutdown
	// can stop accepting commands cleanly.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
	go eng.Run(rootCtx)
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

	// Compose all routes through the shared composition root (see app.NewMux).
	mux := app.NewMux(app.Deps{
		Accounts: accounts, Wallet: wal, Trading: trading, Engine: eng,
		Ledger: led, Hub: hub, Log: log, Faucet: faucet,
		WebhookSecret: envOr("CUSTODY_WEBHOOK_SECRET", "dev-webhook-secret"),
	})

	// Observability + protection at the edge.
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector())
	metrics := httpx.NewMetrics(reg)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// Liveness vs readiness: /healthz is a cheap process check; /readyz verifies
	// the database is reachable so a load balancer only routes once we can serve.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

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
	srv := &http.Server{Addr: addr, Handler: handler}

	// Serve until a termination signal, then drain in-flight requests. The
	// engine journal is fsync'd per command, so no trade is lost on shutdown.
	go func() {
		log.Info("exchange listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server exited", "err", err)
			os.Exit(1)
		}
	}()

	<-rootCtx.Done()
	stop() // restore default signal handling so a second signal force-quits
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
	log.Info("stopped")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
