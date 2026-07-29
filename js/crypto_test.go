package js

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	const passphrase = "correct horse battery staple"
	const plaintext = "the payload, with ñ and emoji 🐦"

	sealed, err := Encrypt(passphrase, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	opened, err := Decrypt(passphrase, sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if opened != plaintext {
		t.Fatalf("Decrypt = %q, want %q", opened, plaintext)
	}
}

func TestEncryptRoundTripsEmptyPlaintext(t *testing.T) {
	sealed, err := Encrypt("key", "")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	opened, err := Decrypt("key", sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if opened != "" {
		t.Fatalf("Decrypt = %q, want an empty string", opened)
	}
}

// A fresh salt and nonce per call mean the same input never produces the same output.
func TestEncryptIsNotDeterministic(t *testing.T) {
	first, err := Encrypt("key", "same plaintext")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	second, err := Encrypt("key", "same plaintext")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if first == second {
		t.Fatal("encrypting the same plaintext twice produced identical ciphertext")
	}
}

func TestDecryptRejectsWrongPassphrase(t *testing.T) {
	sealed, err := Encrypt("right", "secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	opened, err := Decrypt("wrong", sealed)
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Decrypt error = %v, want ErrDecrypt", err)
	}
	if opened != "" {
		t.Fatalf("Decrypt returned %q for a wrong passphrase", opened)
	}
}

// The previous scheme was unauthenticated, so flipping a ciphertext byte silently
// flipped the corresponding plaintext byte. GCM must reject it instead.
func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	sealed, err := Encrypt("key", "transfer 100 to alice")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	raw, err := base64.URLEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("decoding ciphertext: %v", err)
	}
	// Flip a bit inside the sealed body, past the version, salt and nonce.
	raw[len(raw)-1] ^= 0x01
	tampered := base64.URLEncoding.EncodeToString(raw)

	if _, err := Decrypt("key", tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Decrypt error = %v, want ErrDecrypt", err)
	}
}

// Keys were padded with spaces to the AES block size, so these three collided.
func TestPassphrasesThatDifferOnlyByPaddingDoNotCollide(t *testing.T) {
	sealed, err := Encrypt("1234", "secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	for _, passphrase := range []string{"1234            ", "1234 ", "1234\t"} {
		if _, err := Decrypt(passphrase, sealed); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("passphrase %q decrypted a ciphertext sealed with %q", passphrase, "1234")
		}
	}
}

func TestEncryptRejectsEmptyPassphrase(t *testing.T) {
	if _, err := Encrypt("", "secret"); err == nil {
		t.Fatal("Encrypt returned nil error for an empty passphrase")
	}
	if _, err := Decrypt("", "whatever"); err == nil {
		t.Fatal("Decrypt returned nil error for an empty passphrase")
	}
}

func TestDecryptRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"not base64", "!!! not base64 !!!"},
		{"empty", ""},
		{"too short", base64.URLEncoding.EncodeToString([]byte{cryptoVersion, 0x00})},
		{"wrong version", base64.URLEncoding.EncodeToString(make([]byte, 64))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decrypt("key", tt.input); err == nil {
				t.Fatalf("Decrypt(%q) returned nil error", tt.input)
			}
		})
	}
}

func TestDecryptErrorDoesNotLeakPassphrase(t *testing.T) {
	const passphrase = "super-secret-passphrase"
	sealed, err := Encrypt("other", "payload")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = Decrypt(passphrase, sealed)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), passphrase) {
		t.Fatalf("error message contains the passphrase: %v", err)
	}
}
