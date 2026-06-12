// Package app is the composition root for the single-binary backend: it mounts
// the account, wallet, trading, and ops routes onto one HTTP handler. Keeping
// the wiring here (rather than inline in main) lets it be tested directly —
// notably that the /v1/admin/* routes of different services don't shadow each
// other on the shared ServeMux.
package app

import (
	"log/slog"
	"net/http"

	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	accounthttp "github.com/tatanapawan7-ux/bittech/services/account-service/httpapi"
	"github.com/tatanapawan7-ux/bittech/services/exchange"
	"github.com/tatanapawan7-ux/bittech/services/ledger-service/ledger"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/wallet"
	wallethttp "github.com/tatanapawan7-ux/bittech/services/wallet-service/wallethttp"
)

// Deps are the services the HTTP layer is built from.
type Deps struct {
	Accounts      *account.Service
	Wallet        *wallet.Service
	Trading       *exchange.Trading
	Engine        *engine.Engine
	Ledger        *ledger.Service
	Hub           *exchange.Hub
	Log           *slog.Logger
	Faucet        bool
	WebhookSecret string
}

// NewMux assembles the route tree. The wallet admin routes are mounted at their
// exact paths so they don't shadow the exchange's other /v1/admin/* endpoints
// (reconcile, halt, resume), which fall through to the "/" handler.
func NewMux(d Deps) *http.ServeMux {
	mux := http.NewServeMux()

	accountAPI := accounthttp.New(d.Accounts, d.Log)
	for _, route := range []string{"/v1/signup", "/v1/login", "/v1/me", "/v1/2fa/", "/v1/apikeys"} {
		mux.Handle(route, accountAPI)
	}

	walletAPI := wallethttp.New(d.Wallet, d.Accounts, d.Log, d.WebhookSecret)
	for _, route := range []string{
		"/v1/wallet/",
		"/v1/admin/withdrawals",
		"/v1/admin/withdrawals/approve",
		"/v1/admin/withdrawals/reject",
	} {
		mux.Handle(route, walletAPI)
	}

	mux.Handle("/", exchange.NewServer(d.Trading, d.Engine, d.Ledger, d.Accounts, d.Hub, d.Log, d.Faucet))
	return mux
}
