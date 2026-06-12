package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher encrypts secrets that must be recoverable (unlike passwords, which are
// hashed). API-key signing secrets fall in this bucket: HMAC verification needs
// the original secret, so it is stored encrypted with AES-256-GCM rather than
// hashed. In production the key comes from a KMS/Vault; here it is injected.
type Cipher struct {
	aead cipher.AEAD
}

// ErrNoCipher is returned by NewCipher for an invalid key length.
var ErrNoCipher = errors.New("auth: encryption key must be 32 bytes")

// NewCipher builds a Cipher from a 32-byte key (AES-256).
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, ErrNoCipher
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns a base64 string containing a random nonce and the ciphertext.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("auth: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
