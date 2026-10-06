package vaultcipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Cipher encrypts vault secrets at rest (AES-256-GCM). AAD binds each
// ciphertext to userID+"/"+name.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a cipher from a base64-encoded 32-byte key.
func NewCipher(b64 string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("secret key: bad base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key: want 32 bytes, got %d", len(key))
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal returns nonce||ciphertext for plaintext under AAD.
func (c *Cipher) Seal(plaintext []byte, aad string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

// Open decrypts nonce||ciphertext under AAD.
func (c *Cipher) Open(sealed []byte, aad string) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return nil, fmt.Errorf("secrets: ciphertext too short")
	}
	return c.aead.Open(nil, sealed[:n], sealed[n:], []byte(aad))
}
