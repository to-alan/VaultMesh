package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const prefix = "v1:"

type Sealer struct {
	aead cipher.AEAD
}

func New(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must contain exactly 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

func ParseKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("master key is required")
	}
	if key, err := base64.StdEncoding.DecodeString(value); err == nil && len(key) == 32 {
		return key, nil
	}
	if key, err := base64.RawURLEncoding.DecodeString(value); err == nil && len(key) == 32 {
		return key, nil
	}
	if key, err := hex.DecodeString(value); err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, errors.New("master key must be 32 bytes encoded as base64 or hexadecimal")
}

func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := s.aead.Seal(nil, nonce, plaintext, nil)
	payload := append(nonce, ciphertext...)
	return []byte(prefix + base64.RawURLEncoding.EncodeToString(payload)), nil
}

func (s *Sealer) Open(value []byte) ([]byte, error) {
	text := string(value)
	if !strings.HasPrefix(text, prefix) {
		return nil, errors.New("unsupported ciphertext version")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, prefix))
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(payload) < s.aead.NonceSize() {
		return nil, errors.New("ciphertext is too short")
	}
	nonce, ciphertext := payload[:s.aead.NonceSize()], payload[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("decrypt ciphertext")
	}
	return plaintext, nil
}
