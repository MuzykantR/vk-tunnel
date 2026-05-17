//go:build !windows

package wintun

func ensureWintunDLL() error { return nil }
