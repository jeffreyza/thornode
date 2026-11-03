package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

const SaltAtRest = 4

var magicAtRest = [4]byte{0x54, 0x4B, 0x45, 0x01} // "TKE\x01"

func DeriveAtRestKey(seedPhrase string) []byte {
	salted := fmt.Sprintf("%s+%d", seedPhrase, SaltAtRest)
	hash := sha256.Sum256([]byte(salted))
	return hash[:]
}

func EncryptAtRest(key, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create XChaCha20-Poly1305: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, len(magicAtRest)+len(nonce)+len(ciphertext))
	out = append(out, magicAtRest[:]...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

func DecryptAtRest(key, data []byte) ([]byte, error) {
	if !IsEncryptedAtRest(data) {
		return nil, errors.New("data is not at-rest encrypted (missing magic prefix)")
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create XChaCha20-Poly1305: %w", err)
	}

	payload := data[len(magicAtRest):]
	nonceSize := aead.NonceSize()
	if len(payload) < nonceSize+aead.Overhead() {
		return nil, errors.New("at-rest encrypted data too short")
	}

	nonce, ciphertext := payload[:nonceSize], payload[nonceSize:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("at-rest decryption failed: %w", err)
	}
	return plaintext, nil
}

func IsEncryptedAtRest(data []byte) bool {
	if len(data) < len(magicAtRest) {
		return false
	}
	return data[0] == magicAtRest[0] &&
		data[1] == magicAtRest[1] &&
		data[2] == magicAtRest[2] &&
		data[3] == magicAtRest[3]
}
