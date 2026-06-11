package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"time"
)

// API keys follow the model used by Binance-style exchange APIs: a public key
// id identifies the caller, and every request carries an HMAC-SHA256 signature
// computed with a secret that only the client holds. The server stores just the
// SHA-256 digest of the secret, so a database leak does not expose live keys.
//
// Signature payload: "<unix-millis>\n<METHOD>\n<path>\n<body>". The timestamp
// must be within SignatureWindow of server time, which bounds replay attacks.

// SignatureWindow is the maximum allowed clock difference between the signed
// timestamp and server time.
const SignatureWindow = 30 * time.Second

// NewAPIKey generates a key pair: keyID is public, secret is shown to the user
// exactly once, and secretHash (hex SHA-256) is what gets persisted.
func NewAPIKey() (keyID, secret, secretHash string, err error) {
	buf := make([]byte, 48)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	keyID = "bk_" + hex.EncodeToString(buf[:16])
	secret = hex.EncodeToString(buf[16:])
	secretHash = HashSecret(secret)
	return keyID, secret, secretHash, nil
}

// HashSecret returns the hex SHA-256 digest of a secret (API-key secrets and
// session tokens share this storage scheme).
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SignRequest computes the request signature a client must send. Exported so
// the API docs, SDKs, and tests share one implementation.
func SignRequest(secret string, ts time.Time, method, path, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts.UnixMilli(), 10) + "\n" + method + "\n" + path + "\n" + body))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyRequest checks a request signature against the shared secret and
// enforces the freshness window. now is injected for testability.
func VerifyRequest(secret, signature string, tsMillis int64, method, path, body string, now time.Time) bool {
	ts := time.UnixMilli(tsMillis)
	age := now.Sub(ts)
	if age < -SignatureWindow || age > SignatureWindow {
		return false
	}
	want := SignRequest(secret, ts, method, path, body)
	return subtle.ConstantTimeCompare([]byte(want), []byte(signature)) == 1
}

// NewSessionToken generates an opaque bearer token and the hash to persist.
func NewSessionToken() (token, tokenHash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(buf)
	return token, HashSecret(token), nil
}
