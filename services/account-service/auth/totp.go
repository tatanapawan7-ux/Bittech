package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

// TOTP implements RFC 6238 (HMAC-SHA1, 6 digits, 30-second steps) — the variant
// supported by Google Authenticator, Authy, and hardware tokens.
const (
	totpDigits = 6
	totpPeriod = 30 * time.Second
	// totpSkew allows codes from the adjacent time step in either direction to
	// absorb clock drift between the server and the user's device.
	totpSkew = 1
)

// NewTOTPSecret generates a 160-bit base32-encoded shared secret.
func NewTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// TOTPProvisioningURI renders the otpauth:// URI encoded into the QR code that
// authenticator apps scan.
func TOTPProvisioningURI(secret, accountEmail, issuer string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s",
		url.PathEscape(issuer), url.PathEscape(accountEmail), secret, url.QueryEscape(issuer))
}

// TOTPCode computes the code for the time step containing t.
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("auth: bad totp secret: %w", err)
	}
	counter := uint64(t.Unix()) / uint64(totpPeriod.Seconds())
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	// Dynamic truncation per RFC 4226 §5.3.
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, code%1000000), nil
}

// VerifyTOTP reports whether code is valid for the secret at time t, allowing
// ±totpSkew time steps of drift. Comparison is constant time.
func VerifyTOTP(secret, code string, t time.Time) bool {
	for step := -totpSkew; step <= totpSkew; step++ {
		want, err := TOTPCode(secret, t.Add(time.Duration(step)*totpPeriod))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
