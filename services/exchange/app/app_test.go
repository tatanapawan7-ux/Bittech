package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/exchange"
	"github.com/tatanapawan7-ux/bittech/services/matching-engine/engine"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/custody"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/wallet"
)

// TestAdminRoutesDoNotShadow guards against a real bug we shipped once: mounting
// the wallet API under a broad "/v1/admin/" prefix swallowed the exchange's
// reconcile/halt/resume routes. We assert every admin route RESOLVES — i.e.
// returns 401 (auth required) rather than 404 (route not found / shadowed) —
// without needing a database, since auth runs before any handler body.
func TestAdminRoutesDoNotShadow(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	accounts := account.NewService(account.NewMemStore())
	// wallet.New only stores the pool; with no request reaching a handler body
	// (auth fails first) the nil pool is never dereferenced.
	wal := wallet.New(nil, custody.NewMock(), nil, 1)
	eng, err := engine.New([]string{"BTC-USDT"}, &engine.MemJournal{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewMux(Deps{
		Accounts: accounts, Wallet: wal, Engine: eng,
		Trading: exchange.NewTrading(eng, nil), Hub: exchange.NewHub(),
		Log: log, Faucet: true, WebhookSecret: "x",
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Each of these must resolve to its handler (401 from auth), never 404.
	adminRoutes := []struct{ method, path string }{
		{"GET", "/v1/admin/reconcile"},
		{"POST", "/v1/admin/halt"},
		{"POST", "/v1/admin/resume"},
		{"GET", "/v1/admin/withdrawals"},
		{"POST", "/v1/admin/withdrawals/approve"},
		{"POST", "/v1/admin/withdrawals/reject"},
	}
	for _, r := range adminRoutes {
		req, _ := http.NewRequest(r.method, ts.URL+r.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s %s is shadowed (404); route did not resolve", r.method, r.path)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401 (resolved but unauthenticated)", r.method, r.path, resp.StatusCode)
		}
	}

	// A genuinely missing admin route still 404s.
	resp, _ := http.Get(ts.URL + "/v1/admin/does-not-exist")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown admin route: got %d, want 404", resp.StatusCode)
	}
}
