package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type CSRFSigner struct {
	secret []byte
}

func NewCSRFSigner(secret string) *CSRFSigner {
	return &CSRFSigner{secret: []byte(secret)}
}

func (s *CSRFSigner) Token(sessionToken string) string {
	if sessionToken == "" {
		return ""
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *CSRFSigner) Verify(sessionToken, token string) bool {
	want := s.Token(sessionToken)
	if want == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}
