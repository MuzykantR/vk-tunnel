//go:build windows

package wintun

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed dll/wintun-amd64.dll
var wintunDLL []byte

// ensureWintunDLL extracts wintun.dll next to the executable if missing.
// golang.zx2c4.com/wintun loads "wintun.dll" from the application directory.
func ensureWintunDLL() error {
	if len(wintunDLL) == 0 {
		return fmt.Errorf("embedded wintun.dll is empty")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dllPath := filepath.Join(filepath.Dir(exe), "wintun.dll")
	if _, err := os.Stat(dllPath); err == nil {
		return nil
	}
	return os.WriteFile(dllPath, wintunDLL, 0o644)
}
