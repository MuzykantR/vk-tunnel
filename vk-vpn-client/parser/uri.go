package parser

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const MASTER_KEY_HEX = "5c34e8f9b2d1a0c746e5f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9"

type VPNPayload struct {
	Link     string `json:"link"`
	TS       int64  `json:"ts"`
	ServerPK string `json:"server_pk"`
}

// ParseInputLink accepts either the encrypted `myvpn://` envelope used
// in production OR a raw VK call join-link copied straight from the
// server daemon log. The raw-link branch exists for the RU-VPS
// diagnostic build (branch diag/ru-vps-test): there is no
// Telegram-bot in the loop so the operator simply pastes the URL from
// `make logs-daemon` into the Wails client.
//
// This is a diagnostic branch only — the production build keeps the
// strict myvpn:// requirement (`ParseAndDecryptURI`).
func ParseInputLink(input string) (*VPNPayload, error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "myvpn://") {
		return ParseAndDecryptURI(input)
	}
	if isVKJoinLink(input) {
		log.Printf("[diag] raw VK join-link accepted (diag/ru-vps-test build)")
		return &VPNPayload{Link: input, TS: time.Now().Unix()}, nil
	}
	return nil, errors.New("invalid URI: expected myvpn:// or https://vk.com/call/join/<token>")
}

// isVKJoinLink recognises the canonical VK call invite URLs. The
// trailing token must be non-empty and contain no whitespace.
func isVKJoinLink(s string) bool {
	for _, prefix := range []string{
		"https://vk.com/call/join/",
		"https://vk.ru/call/join/",
		"http://vk.com/call/join/",
	} {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		tail := strings.TrimRight(strings.TrimPrefix(s, prefix), "/")
		if tail == "" || strings.ContainsAny(tail, " \t\r\n") {
			return false
		}
		return true
	}
	return false
}

// ParseAndDecryptURI extracts the payload from a myvpn:// URI.
func ParseAndDecryptURI(uri string) (*VPNPayload, error) {
	if !strings.HasPrefix(uri, "myvpn://") {
		return nil, errors.New("invalid URI scheme")
	}

	body := strings.TrimPrefix(uri, "myvpn://")
	parts := strings.Split(body, ":")
	if len(parts) != 3 {
		return nil, errors.New("invalid URI format, expected version:iv:ciphertext")
	}

	version := parts[0]
	if version != "v1" {
		return nil, fmt.Errorf("unsupported protocol version: %s", version)
	}

	ivB64 := parts[1]
	cipherB64 := parts[2]

	// 1. Decode IV and Ciphertext from Base64URL (unpadded)
	iv, err := base64.RawURLEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode IV: %w", err)
	}

	ciphertext, err := base64.RawURLEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	key, err := MasterKeyBytes()
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}

	// 3. Initialize AES-GCM
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// 4. Decrypt (Open)
	plaintext, err := aesgcm.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt payload: %w", err)
	}

	// 5. Unmarshal JSON
	var payload VPNPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse decrypted JSON: %w", err)
	}

	// Validate timestamp (e.g., links older than 24 hours are rejected)
	now := time.Now().Unix()
	if now-payload.TS > 86400 {
		return nil, errors.New("the VPN link has expired (older than 24 hours)")
	}
	// Also prevent future timestamps by a reasonable margin
	if payload.TS > now+300 {
		return nil, errors.New("the VPN link has an invalid future timestamp")
	}

	return &payload, nil
}
