// Package httpapi exposes the account service over HTTP/JSON.
//
// Endpoints (all request/response bodies are JSON):
//
//	POST /v1/signup       {email, password}            -> {user_id, email}
//	POST /v1/login        {email, password, totp_code} -> {token}
//	GET  /v1/me           Authorization: Bearer <tok>  -> {user_id, email, totp_enabled}
//	POST /v1/2fa/setup    (auth)                       -> {secret, uri}
//	POST /v1/2fa/confirm  (auth) {code}                -> {enabled: true}
//	POST /v1/apikeys      (auth) {label}               -> {key_id, secret}  (secret shown once)
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tatanapawan7-ux/bittech/services/account-service/account"
)

// Server routes HTTP requests to the account service.
type Server struct {
	svc *account.Service
	mux *http.ServeMux
	log *slog.Logger
}

// New builds the HTTP server around an account service.
func New(svc *account.Service, log *slog.Logger) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux(), log: log}
	s.mux.HandleFunc("POST /v1/signup", s.handleSignup)
	s.mux.HandleFunc("POST /v1/login", s.handleLogin)
	s.mux.HandleFunc("GET /v1/me", s.authed(s.handleMe))
	s.mux.HandleFunc("POST /v1/2fa/setup", s.authed(s.handle2FASetup))
	s.mux.HandleFunc("POST /v1/2fa/confirm", s.authed(s.handle2FAConfirm))
	s.mux.HandleFunc("POST /v1/apikeys", s.authed(s.handleCreateAPIKey))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// authed wraps a handler with bearer-token authentication.
func (s *Server) authed(next func(http.ResponseWriter, *http.Request, *account.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		u, err := s.svc.Authenticate(r.Context(), token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		next(w, r, u)
	}
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct{ Email, Password string }
	if !decode(w, r, &req) {
		return
	}
	u, err := s.svc.Signup(r.Context(), req.Email, req.Password)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": u.ID, "email": u.Email})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, _, err := s.svc.Login(r.Context(), req.Email, req.Password, req.TOTPCode)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, u *account.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": u.ID, "email": u.Email, "totp_enabled": u.TOTPEnabled,
	})
}

func (s *Server) handle2FASetup(w http.ResponseWriter, r *http.Request, u *account.User) {
	secret, uri, err := s.svc.SetupTOTP(r.Context(), u.ID)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "uri": uri})
}

func (s *Server) handle2FAConfirm(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct{ Code string }
	if !decode(w, r, &req) {
		return
	}
	if err := s.svc.ConfirmTOTP(r.Context(), u.ID, req.Code); err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true})
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, u *account.User) {
	var req struct{ Label string }
	if !decode(w, r, &req) {
		return
	}
	keyID, secret, err := s.svc.CreateAPIKey(r.Context(), u.ID, req.Label)
	if err != nil {
		s.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key_id": keyID, "secret": secret})
}

// writeServiceErr maps service errors to HTTP statuses without leaking internals.
func (s *Server) writeServiceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrEmailTaken):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, account.ErrBadEmail), errors.Is(err, account.ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, account.ErrTOTPRequired):
		writeErr(w, http.StatusUnauthorized, "totp code required")
	case errors.Is(err, account.ErrInvalidCredentials):
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
	default:
		s.log.Error("internal error", "err", err)
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
