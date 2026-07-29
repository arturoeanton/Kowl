package js

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// Ciphertext layout, base64url encoded:
//
//	version(1) || salt(saltSize) || nonce(gcmNonceSize) || AES-256-GCM ciphertext+tag
//
// The passphrase is stretched with PBKDF2 rather than used as a key directly, and GCM
// authenticates the result, so tampering is detected instead of silently decrypting to
// garbage.
const (
	cryptoVersion = 1
	saltSize      = 16
	keySize       = 32
	// pbkdf2Iterations follows the OWASP recommendation for PBKDF2-HMAC-SHA256.
	pbkdf2Iterations = 210000
)

// ErrDecrypt is returned when a ciphertext cannot be authenticated: a wrong
// passphrase, a corrupted payload or a deliberate modification all look the same.
var ErrDecrypt = errors.New("cannot decrypt: wrong passphrase or corrupted ciphertext")

// Encrypt seals plaintext under passphrase and returns a base64url string.
func Encrypt(passphrase, plaintext string) (string, error) {
	if passphrase == "" {
		return "", errors.New("passphrase must not be empty")
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	payload := make([]byte, 0, 1+len(salt)+len(nonce)+len(plaintext)+gcm.Overhead())
	payload = append(payload, cryptoVersion)
	payload = append(payload, salt...)
	payload = append(payload, nonce...)
	payload = gcm.Seal(payload, nonce, []byte(plaintext), nil)

	return base64.URLEncoding.EncodeToString(payload), nil
}

// Decrypt opens a ciphertext produced by Encrypt.
func Decrypt(passphrase, ciphertext string) (string, error) {
	if passphrase == "" {
		return "", errors.New("passphrase must not be empty")
	}

	payload, err := base64.URLEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decoding ciphertext: %w", err)
	}
	if len(payload) < 1 || payload[0] != cryptoVersion {
		return "", fmt.Errorf("unsupported ciphertext format")
	}
	payload = payload[1:]
	if len(payload) < saltSize {
		return "", ErrDecrypt
	}

	salt, rest := payload[:saltSize], payload[saltSize:]
	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return "", err
	}
	if len(rest) < gcm.NonceSize() {
		return "", ErrDecrypt
	}

	nonce, sealed := rest[:gcm.NonceSize()], rest[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plaintext), nil
}

func newGCM(passphrase string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, pbkdf2Iterations, keySize)
	if err != nil {
		return nil, fmt.Errorf("deriving key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return gcm, nil
}
