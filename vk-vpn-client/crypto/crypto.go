package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// MasterKey should be exactly 32 bytes (256-bit).
// In a real app, this can be injected at build time via -ldflags "-X ...".
const MasterKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

var masterKey []byte

func init() {
	var err error
	masterKey, err = hex.DecodeString(MasterKeyHex)
	if err != nil || len(masterKey) != 32 {
		panic("Invalid MASTER_KEY in client build")
	}
}

// DecryptPayload decrypts the GCM payload.
// ivB64 and cipherB64 are base64url strings without padding.
func DecryptPayload(ivB64, cipherB64 string) ([]byte, error) {
	iv, err := base64.RawURLEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, fmt.Errorf("invalid IV base64: %w", err)
	}

	ciphertext, err := base64.RawURLEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext base64: %w", err)
	}

	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(iv) != aesgcm.NonceSize() {
		return nil, errors.New("invalid IV length")
	}

	// In Go, aesgcm.Open expects the ciphertext and the auth tag appended together.
	// This matches what the Python cryptography library produces by default.
	plaintext, err := aesgcm.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong key or corrupted data): %w", err)
	}

	return plaintext, nil
}
