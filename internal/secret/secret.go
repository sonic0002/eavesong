// Package secret encrypts API keys and tokens before they are stored, so a
// copied database file alone does not reveal them.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Box seals and opens values with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

const prefix = "v1:"

// LoadKey returns the encryption key: EAVESONG_SECRET_KEY (base64, 32 bytes)
// when set, otherwise the key file next to the database, created on first run
// with owner-only permissions. Keep that file with backups of the database.
func LoadKey(keyFile string) ([]byte, error) {
	v := strings.TrimSpace(os.Getenv("EAVESONG_SECRET_KEY"))
	if v != "" {
		key, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(key) != 32 {
			return nil, errors.New("EAVESONG_SECRET_KEY must be 32 random bytes, base64-encoded (openssl rand -base64 32)")
		}
		return key, nil
	}
	if data, err := os.ReadFile(keyFile); err == nil {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s is not a valid key file", keyFile)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("creating the key file: %w", err)
	}
	return key, nil
}

func New(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts a value; the empty string stays empty.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return prefix + base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open decrypts a value made by Seal.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, prefix) {
		return "", errors.New("not an encrypted value")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil || len(data) < b.aead.NonceSize() {
		return "", errors.New("damaged encrypted value")
	}
	plain, err := b.aead.Open(nil, data[:b.aead.NonceSize()], data[b.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot decrypt a stored secret; was the key file replaced?")
	}
	return string(plain), nil
}
