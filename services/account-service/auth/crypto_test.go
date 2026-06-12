package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	secret := "super-secret-signing-key"
	enc, err := c.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	if enc == secret {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := c.Decrypt(enc)
	if err != nil || got != secret {
		t.Fatalf("decrypt: %q %v", got, err)
	}
}

func TestCipherNonceIsRandom(t *testing.T) {
	c, _ := NewCipher(make([]byte, 32))
	a, _ := c.Encrypt("x")
	b, _ := c.Encrypt("x")
	if a == b {
		t.Fatal("encrypting the same plaintext twice must differ (random nonce)")
	}
}

func TestCipherRejectsBadKey(t *testing.T) {
	if _, err := NewCipher(make([]byte, 16)); !errors.Is(err, ErrNoCipher) {
		t.Fatalf("expected ErrNoCipher, got %v", err)
	}
}

func TestCipherTamperDetected(t *testing.T) {
	c, _ := NewCipher(make([]byte, 32))
	enc, _ := c.Encrypt("data")
	// Flip the last byte of the base64 — decryption must fail (GCM auth).
	raw := []byte(enc)
	raw[len(raw)-1] ^= 0x01
	if _, err := c.Decrypt(string(raw)); err == nil {
		t.Fatal("tampered ciphertext should not decrypt")
	}
	_ = bytes.Equal
}
