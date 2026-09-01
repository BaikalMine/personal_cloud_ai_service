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
	return c.EncryptBytesWithAAD(plain, nil)
}

func (c *Cipher) EncryptBytesWithAAD(plain, additionalData []byte) ([]byte, error) {
	return c.EncryptBytesWithAADInto(nil, plain, additionalData)
}

func (c *Cipher) EncryptBytesWithAADInto(destination, plain, additionalData []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	requiredCapacity := nonceSize + len(plain) + c.aead.Overhead()
	if cap(destination) < requiredCapacity {
		destination = make([]byte, nonceSize, requiredCapacity)
	} else {
		destination = destination[:nonceSize]
	}
	nonce := destination[:nonceSize]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate content nonce: %w", err)
	}
	return c.aead.Seal(destination, nonce, plain, additionalData), nil
}

func (c *Cipher) Decrypt(encrypted []byte) (string, error) {
	plain, err := c.DecryptBytes(encrypted)
	return string(plain), err
}

func (c *Cipher) DecryptBytes(encrypted []byte) ([]byte, error) {
	return c.DecryptBytesWithAAD(encrypted, nil)
}

func (c *Cipher) DecryptBytesWithAAD(encrypted, additionalData []byte) ([]byte, error) {
	return c.DecryptBytesWithAADInto(nil, encrypted, additionalData)
}

func (c *Cipher) DecryptBytesWithAADInto(destination, encrypted, additionalData []byte) ([]byte, error) {
	if len(encrypted) < c.aead.NonceSize() {
		return nil, fmt.Errorf("encrypted content is truncated")
	}
	nonce, payload := encrypted[:c.aead.NonceSize()], encrypted[c.aead.NonceSize():]
	plain, err := c.aead.Open(destination[:0], nonce, payload, additionalData)
	if err != nil {
		return nil, fmt.Errorf("decrypt content: %w", err)
	}
	return plain, nil
}
