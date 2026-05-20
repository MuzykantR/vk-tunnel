package tunconfig

import (
	"os"
	"strconv"
)

const DefaultMTU = 1400

// MTUFromEnv reads VK_VPN_MTU (default 1400, clamp 1280–1500).
func MTUFromEnv() int {
	mtu := DefaultMTU
	if v := os.Getenv("VK_VPN_MTU"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			mtu = n
		}
	}
	if mtu < 1280 {
		return 1280
	}
	if mtu > 1500 {
		return 1500
	}
	return mtu
}
