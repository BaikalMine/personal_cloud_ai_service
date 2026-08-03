package security

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	MinPasswordLength  = 10
	MaxPasswordLength  = 256
	passwordHashPrefix = "$bcrypt-sha256$"
)

var (
	ErrPasswordTooShort = errors.New("password must be at least 10 characters")
	ErrPasswordTooLong  = errors.New("password must be no longer than 256 characters")
)

var dummyPasswordHash = func() string {
	hash, err := bcrypt.GenerateFromPassword(passwordBcryptInput("timing-equalization-password"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return passwordHashPrefix + string(hash)
}()

func ValidatePassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(password) > MaxPasswordLength:
		return ErrPasswordTooLong
	default:
		return nil
	}
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword(passwordBcryptInput(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return passwordHashPrefix + string(hash), nil
}

func VerifyPassword(hash, password string) bool {
	if len(password) > MaxPasswordLength {
		return false
	}
	hashValue := hash
	passwordInput := []byte(password)
	if hash == "" {
		hashValue = dummyPasswordHash
	}
	if strings.HasPrefix(hashValue, passwordHashPrefix) {
		hashValue = strings.TrimPrefix(hashValue, passwordHashPrefix)
		passwordInput = passwordBcryptInput(password)
	}
	return bcrypt.CompareHashAndPassword([]byte(hashValue), passwordInput) == nil
}

func PasswordHashNeedsUpgrade(hash string) bool {
	if hash == "" || !strings.HasPrefix(hash, passwordHashPrefix) {
		return hash != ""
	}
	cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(hash, passwordHashPrefix)))
	return err != nil || cost < bcrypt.DefaultCost
}

func passwordBcryptInput(password string) []byte {
	digest := sha256.Sum256([]byte(password))
	encoded := make([]byte, base64.RawStdEncoding.EncodedLen(len(digest)))
	base64.RawStdEncoding.Encode(encoded, digest[:])
	return encoded
}
