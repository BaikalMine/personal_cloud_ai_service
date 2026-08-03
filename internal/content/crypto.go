package content

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(secret string) (*Cipher, error) {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("ai-access-gateway:content:v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plain string) ([]byte, error) {
	return c.EncryptBytes([]byte(plain))
}

func (c *Cipher) EncryptBytes(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate content nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plain, nil), nil
}

func (c *Cipher) Decrypt(encrypted []byte) (string, error) {
	plain, err := c.DecryptBytes(encrypted)
	return string(plain), err
}

func (c *Cipher) DecryptBytes(encrypted []byte) ([]byte, error) {
	if len(encrypted) < c.aead.NonceSize() {
		return nil, fmt.Errorf("encrypted content is truncated")
	}
	nonce, payload := encrypted[:c.aead.NonceSize()], encrypted[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt content: %w", err)
	}
	return plain, nil
}
