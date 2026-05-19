package parser

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// MasterKeyBytes returns 32-byte AES key from MASTER_KEY_HEX or MASTER_KEY (64 hex chars).
// Falls back to compiled MASTER_KEY_HEX constant with a warning if env unset.
func MasterKeyBytes() ([]byte, error) {
	hexKey := strings.TrimSpace(os.Getenv("MASTER_KEY_HEX"))
	if hexKey == "" {
		hexKey = strings.TrimSpace(os.Getenv("MASTER_KEY"))
	}
	if hexKey == "" {
		key, err := hex.DecodeString(MASTER_KEY_HEX)
		if err != nil {
			return nil, fmt.Errorf("built-in master key invalid: %w", err)
		}
		return key, nil
	}
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("MASTER_KEY must be 64 hex characters (32 bytes), got %d", len(hexKey))
	}
	return hex.DecodeString(hexKey)
}
