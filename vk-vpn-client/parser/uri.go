package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vk-vpn/client/crypto"
)

type VPNPayload struct {
	Link     string `json:"link"`
	TS       int64  `json:"ts"`
	ServerPK string `json:"server_pk"`
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
	ivB64 := parts[1]
	cipherB64 := parts[2]

	if version != "v1" {
		return nil, fmt.Errorf("unsupported protocol version: %s", version)
	}

	plaintext, err := crypto.DecryptPayload(ivB64, cipherB64)
	if err != nil {
		return nil, err
	}

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
