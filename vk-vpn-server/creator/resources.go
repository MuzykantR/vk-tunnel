package creator

import (
	"log"
	"runtime/debug"
)

// ResourceProfile mirrors whitelist-bypass headless --resources modes.
type ResourceProfile struct {
	ReadBuf  int
	MaxDCBuf uint64
	MemLimit int64
}

const RTPBufSize = 65536

func ParseResources(mode string, customReadBuf int, customMaxDCBuf int, customMemLimit int64) ResourceProfile {
	var p ResourceProfile
	switch mode {
	case "moderate":
		p.ReadBuf = 16 * 1024
		p.MaxDCBuf = 1 * 1024 * 1024
		p.MemLimit = 64 * 1024 * 1024
	case "default":
		p.ReadBuf = 32 * 1024
		p.MaxDCBuf = 4 * 1024 * 1024
		p.MemLimit = 128 * 1024 * 1024
	case "unlimited":
		p.ReadBuf = RTPBufSize
		p.MaxDCBuf = 8 * 1024 * 1024
		p.MemLimit = 256 * 1024 * 1024
	case "custom":
		p.ReadBuf = customReadBuf
		if p.ReadBuf == 0 {
			p.ReadBuf = RTPBufSize
		}
		p.MaxDCBuf = uint64(customMaxDCBuf)
		if p.MaxDCBuf == 0 {
			p.MaxDCBuf = 8 * 1024 * 1024
		}
		p.MemLimit = customMemLimit
		if p.MemLimit == 0 {
			p.MemLimit = 256 * 1024 * 1024
		}
	default:
		log.Printf("[config] unknown resources %q, using default", mode)
		return ParseResources("default", 0, 0, 0)
	}
	if p.MemLimit > 0 {
		debug.SetMemoryLimit(p.MemLimit)
	}
	log.Printf("[config] resources=%s read-buf=%d max-dc-buf=%d mem-limit=%d", mode, p.ReadBuf, p.MaxDCBuf, p.MemLimit)
	return p
}
