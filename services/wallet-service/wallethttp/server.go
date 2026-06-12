// Package wallethttp exposes the wallet service over HTTP/JSON.
//
// User endpoints (bearer auth; sensitive ones also require a fresh TOTP code):
//
//	GET  /v1/wallet/address?asset=BTC            -> {address}
//	POST /v1/wallet/allowlist  {asset,address,label,totp_code}
//	POST /v1/wallet/withdraw   {asset,address,amount,totp_code}
//
// Operator endpoints (admin role):
//
//	GET  /v1/admin/withdrawals                    -> pending withdrawals
//	POST /v1/admin/withdrawals/approve {id}
//	POST /v1/admin/withdrawals/reject  {id}
//
// Custody webhook (shared-secret header), called by the provider on chain events:
//
//	POST /v1/wallet/webhook/deposit  {asset,address,tx_hash,amount,confirmations}
package wallethttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
	"github.com/tatanapawan7-ux/bittech/services/wallet-service/wallet"
)

// Authorizer authenticates bearer tokens and verifies step-up TOTP. The account
// service satisfies it; kept as an interface so the wallet HTTP layer does not
// reach into account internals.
type Authorizer interface {
	Authenticate(ctx context.Context, token string) (*account.User, error)
	RequireTOTP(ctx context.Context, userID int64, code string) error
}

// Server routes wallet HTTP requests.
type Server struct {
	wallet        *wallet.Service
	auth          Authorizer
	mux           *http.ServeMux
	log           *slog.Logger
	webhookSecret string
}

// New wires the wallet routes. webhookSecret authenticates custody callbacks.
func New(w *wallet.Service, auth Authorizer, log *slog.Logger, webhookSecret string) *Server {
	s := &Server{wallet: w, auth: auth, mux: http.NewServeMux(), log: log, webhookSecret: webhookSecret}
	s.mux.HandleFunc("GET /v1/wallet/address", s.authed(s.handleAddress))
	s.mux.HandleFunc("POST /v1/wallet/allowlist", s.authed(s.handleAllowlist))
	s.mux.HandleFunc("POST /v1/wallet/withdraw", s.authed(s.handleWithdraw))
	s.mux.HandleFunc("GET /v1/admin/withdrawals", s.adminOnly(s.handlePending))
	s.mux.HandleFunc("POST /v1/admin/withdrawals/approve", s.adminOnly(s.handleApprove))
	s.mux.HandleFunc("POST /v1/admin/withdrawals/reject", s.adminOnly(s.handleReject))
	s.mux.HandleFunc("POST /v1/wallet/webhook/deposit", s.handleDepositWebhook)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) authed(next func(http.ResponseWriter, *http.Request, *account.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.user(w, r)
		if !ok {
			return
		}
		next(w, r, u)
	}
}

func (s *Server) adminOnly(next func(http.ResponseWriter, *http.Request, *account.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.user(w, r)
		if !ok {
			return
		}
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r, u)
	}
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) (*account.User, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		writeErr(w, http.StatusUnauthorized, "missing bearer token")
		return nil, false
	}
	u, err := s.auth.Authenticate(r.Context(), token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid or expired session")
		return nil, false
	}
	return u, true
}

func (s *Server) handleAddress(w http.ResponseWriter, r *http.Request, u *account.User) {
	asset := r.URL.Query().Get("asset")
	addr, err := s.wallet.DepositAddress(r.Context(), u.ID, asset)
	if err != nil {
		s.log.Error("deposit address", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"asset": asset, "address": addr})
}

func (s *Server) handleAllowlist(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct{ Asset, Address, Label, TOTPCode string }
	if !decode(w, r, &req) {
		return
	}
	if !s.stepUp(w, r, u, req.TOTPCode) {
		return
	}
	if err := s.wallet.AddAllowlistAddress(r.Context(), u.ID, req.Asset, req.Address, req.Label); err != nil {
		s.log.Error("allowlist", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleWithdraw(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct {
		Asset    string `json:"asset"`
		Address  string `json:"address"`
		Amount   int64  `json:"amount"`
		TOTPCode string `json:"totp_code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !s.stepUp(w, r, u, req.TOTPCode) {
		return
	}
	wd, err := s.wallet.RequestWithdrawal(r.Context(), u.ID, req.Asset, req.Address, req.Amount)
	if err != nil {
		s.writeWalletErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wd)
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request, _ *account.User) {
	list, err := s.wallet.PendingWithdrawals(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"withdrawals": list})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request, _ *account.User) {
	s.decide(w, r, s.wallet.ApproveWithdrawal)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request, _ *account.User) {
	s.decide(w, r, s.wallet.RejectWithdrawal)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request, fn func(context.Context, int64) (*wallet.Withdrawal, error)) {
	var req struct {
		ID int64 `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	wd, err := fn(r.Context(), req.ID)
	if err != nil {
		s.writeWalletErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wd)
}

// handleDepositWebhook is the custody provider callback. It is authenticated by
// a shared secret header rather than a user session.
func (s *Server) handleDepositWebhook(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Webhook-Secret")), []byte(s.webhookSecret)) != 1 {
		writeErr(w, http.StatusUnauthorized, "bad webhook secret")
		return
	}
	var req struct {
		Asset         string `json:"asset"`
		Address       string `json:"address"`
		TxHash        string `json:"tx_hash"`
		Amount        int64  `json:"amount"`
		Confirmations int    `json:"confirmations"`
	}
	if !decode(w, r, &req) {
		return
	}
	credited, err := s.wallet.HandleDeposit(r.Context(), req.Asset, req.Address, req.TxHash, req.Amount, req.Confirmations)
	if err != nil {
		s.log.Error("deposit webhook", "err", err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credited": credited})
}

// stepUp enforces a valid TOTP code for sensitive actions, writing the error
// response and returning false on failure.
func (s *Server) stepUp(w http.ResponseWriter, r *http.Request, u *account.User, code string) bool {
	err := s.auth.RequireTOTP(r.Context(), u.ID, code)
	switch {
	case err == nil:
		return true
	case errors.Is(err, account.ErrTOTPRequired):
		writeErr(w, http.StatusForbidden, "enable 2FA before withdrawing")
	default:
		writeErr(w, http.StatusUnauthorized, "invalid 2FA code")
	}
	return false
}

func (s *Server) writeWalletErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wallet.ErrNotAllowlisted):
		writeErr(w, http.StatusForbidden, "destination not allowlisted")
	case errors.Is(err, wallet.ErrOverLimit), errors.Is(err, wallet.ErrBadAmount):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, wallet.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, wallet.ErrNotPending):
		writeErr(w, http.StatusConflict, "already decided")
	default:
		s.log.Error("wallet error", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
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
