package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	CookiesPath string
	APIPort     int
	CallID      string // Optional: if empty, daemon generates a new one
}

type CookieInfo struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

func LoadCookies(path string) ([]CookieInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read cookies file: %w", err)
	}

	var cookies []CookieInfo
	if err := json.Unmarshal(data, &cookies); err != nil {
		return nil, fmt.Errorf("failed to parse cookies: %w", err)
	}

	return cookies, nil
}
