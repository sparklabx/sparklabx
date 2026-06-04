package services

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := []byte("test-key-please-rotate-in-prod")
	cases := []string{
		"",
		"a",
		"hello world",
		strings.Repeat("x", 4096),
		"unicode: 你好 世界 \U0001f600",
	}
	for _, plaintext := range cases {
		encoded, err := Encrypt(plaintext, key)
		if err != nil {
			t.Fatalf("Encrypt(%q) failed: %v", plaintext, err)
		}
		decoded, err := Decrypt(encoded, key)
		if err != nil {
			t.Fatalf("Decrypt for %q failed: %v", plaintext, err)
		}
		if decoded != plaintext {
			t.Fatalf("roundtrip mismatch: got %q want %q", decoded, plaintext)
		}
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	key := []byte("test-key")
	a, err := Encrypt("same plaintext", key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt("same plaintext", key)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext; nonce is not random")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	plaintext := "sensitive minio secret"
	encoded, err := Encrypt(plaintext, []byte("correct-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(encoded, []byte("attacker-key")); err == nil {
		t.Fatal("Decrypt succeeded with wrong key; should have failed")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	key := []byte("test-key")
	encoded, err := Encrypt("important payload", key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a bit in the last byte (inside the GCM auth tag region).
	raw[len(raw)-1] ^= 0x01
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := Decrypt(tampered, key); err == nil {
		t.Fatal("Decrypt accepted tampered ciphertext; GCM auth tag not enforced")
	}
}

func TestDecryptRejectsShortInput(t *testing.T) {
	if _, err := Decrypt(base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), []byte("k")); err == nil {
		t.Fatal("Decrypt accepted truncated input")
	}
}

func TestDecryptRejectsInvalidBase64(t *testing.T) {
	if _, err := Decrypt("!!!not-base64!!!", []byte("k")); err == nil {
		t.Fatal("Decrypt accepted invalid base64")
	}
}
