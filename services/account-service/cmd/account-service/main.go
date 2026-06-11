// Command account-service runs the account HTTP API.
//
// Configuration via environment:
//
//	DATABASE_URL  Postgres connection string. If unset, an in-memory store is
//	              used (local development only — state is lost on restart).
//	LISTEN_ADDR   HTTP listen address (default ":8081").
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/account-service/httpapi"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	var store account.Store
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			log.Error("connect postgres", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		store = account.NewPGStore(pool)
		log.Info("using postgres store")
	} else {
		store = account.NewMemStore()
		log.Warn("DATABASE_URL unset; using in-memory store (dev only)")
	}

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	srv := httpapi.New(account.NewService(store), log)
	log.Info("account-service listening", "addr", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}
