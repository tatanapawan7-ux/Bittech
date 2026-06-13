package wallethttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/custody"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/wallet"
)

// stubAuth records the TOTP code handed to RequireTOTP and always fails it, so
// handlers short-circuit at the 2FA step before touching the (nil) database.
type stubAuth struct{ gotCode string }

func (a *stubAuth) Authenticate(_ context.Context, _ string) (*account.User, error) {
	return &account.User{ID: 1, TOTPEnabled: true}, nil
}
func (a *stubAuth) RequireTOTP(_ context.Context, _ int64, code string) error {
	a.gotCode = code
	return account.ErrInvalidCredentials
}

func newTestServer(auth Authorizer) *httptest.Server {
	w := wallet.New(nil, custody.NewMock(), nil, 1) // never reached: 2FA fails first
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptest.NewServer(New(w, auth, log, "secret"))
}

// TestStepUpEndpointsBindTOTPCode guards against the JSON-binding bug where a
// request struct field without a `json:"totp_code"` tag silently left the code
// empty, breaking 2FA-gated actions.
func TestStepUpEndpointsBindTOTPCode(t *testing.T) {
	cases := []struct {
		path string
		body string
	}{
		{"/v1/wallet/allowlist", `{"asset":"BTC","address":"a","label":"l","totp_code":"654321"}`},
		{"/v1/wallet/withdraw", `{"asset":"BTC","address":"a","amount":10,"totp_code":"654321"}`},
	}
	for _, c := range cases {
		auth := &stubAuth{}
		ts := newTestServer(auth)
		req, _ := http.NewRequest("POST", ts.URL+c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer t")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		ts.Close()
		if auth.gotCode != "654321" {
			t.Errorf("%s: TOTP code did not bind; RequireTOTP got %q, want %q", c.path, auth.gotCode, "654321")
		}
	}
}

// TestStepUpRejectsWithoutCode confirms a missing/invalid code is a 401, not a
// silent pass to the handler body.
func TestStepUpRejectsWithoutCode(t *testing.T) {
	auth := &stubAuth{}
	ts := newTestServer(auth)
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/wallet/allowlist",
		strings.NewReader(`{"asset":"BTC","address":"a"}`))
	req.Header.Set("Authorization", "Bearer t")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing code should 401, got %d", resp.StatusCode)
	}
}
