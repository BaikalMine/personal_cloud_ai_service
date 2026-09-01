package content

import (
	"bytes"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	cipher, err := NewCipher("01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("секретный prompt")
	if err != nil {
		t.Fatal(err)
	}
	if string(encrypted) == "секретный prompt" {
		t.Fatal("content was stored as plaintext")
	}
	plain, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "секретный prompt" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestCipherAdditionalDataBindsMediaChunk(t *testing.T) {
	cipher, err := NewCipher("01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("media chunk")
	encrypted, err := cipher.EncryptBytesWithAAD(plain, []byte("media:7:0"))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := cipher.DecryptBytesWithAAD(encrypted, []byte("media:7:0"))
	if err != nil || !bytes.Equal(decrypted, plain) {
		t.Fatalf("AAD round trip=%q err=%v", decrypted, err)
	}
	if _, err := cipher.DecryptBytesWithAAD(encrypted, []byte("media:7:1")); err == nil {
		t.Fatal("media chunk decrypted with a different chunk identity")
	}
	encryptBuffer := make([]byte, 0, len(encrypted)+32)
	reusedEncrypted, err := cipher.EncryptBytesWithAADInto(encryptBuffer, plain, []byte("media:7:2"))
	if err != nil || cap(reusedEncrypted) != cap(encryptBuffer) {
		t.Fatalf("encrypt buffer was not reused: cap=%d want=%d err=%v", cap(reusedEncrypted), cap(encryptBuffer), err)
	}
	decryptBuffer := make([]byte, 0, len(plain)+32)
	reusedPlain, err := cipher.DecryptBytesWithAADInto(decryptBuffer, reusedEncrypted, []byte("media:7:2"))
	if err != nil || !bytes.Equal(reusedPlain, plain) || cap(reusedPlain) != cap(decryptBuffer) {
		t.Fatalf("decrypt buffer was not reused: plain=%q cap=%d want=%d err=%v", reusedPlain, cap(reusedPlain), cap(decryptBuffer), err)
	}
}
