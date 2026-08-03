package content

import "testing"

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
