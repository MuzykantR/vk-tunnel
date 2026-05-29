package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vk-vpn/server/api"
	"github.com/vk-vpn/server/config"
	"github.com/vk-vpn/server/creator"
	"github.com/vk-vpn/server/daemon"

	"github.com/vk-vpn/client/logx"
)

// diagBannerLines is printed line-by-line via logx so every line gets
// the project's standard `HH:MM:SS.mmm [tag]` prefix and routes
// through the same log-level gate as the rest of the daemon. Picked
// up by `make logs-daemon` / journald.
var diagBannerLines = []string{
	"*****************************************************************",
	"*** DIAG BUILD — branch diag/ru-vps-test                      ***",
	"*** RU-VPS cap-localisation test (Scenario A vs B/B.1/B.2).   ***",
	"*** Do NOT deploy this binary to production VPSes.            ***",
	"*****************************************************************",
}

func main() {
	logx.Init()
	for _, line := range diagBannerLines {
		logx.Warn("boot", "%s", line)
	}
	cookiesPath := flag.String("cookies", "cookies.json", "Path to cookies.json file")
	apiPort := flag.Int("port", 8080, "Local API port for the Bot")
	resources := flag.String("resources", "default", "Resource mode: moderate | default | unlimited | custom (whitelist-bypass)")
	customReadBuf := flag.Int("read-buf", 0, "TCP/DC read buffer with --resources custom")
	customMaxDCBuf := flag.Int("max-dc-buf", 0, "DC BufferedAmount threshold with --resources custom")
	customMemLimit := flag.Int64("mem-limit", 0, "Go soft memory limit with --resources custom")
	callTTL := flag.Duration("call-ttl", 2*time.Hour, "Rotate VK call after this duration (roadmap antifraud)")
	logFile := flag.String("log-file", os.Getenv("VK_VPN_LOG_FILE"),
		"Write log to this file (truncated on every start). Empty = stderr/journald only. "+
			"Default: $VK_VPN_LOG_FILE.")
	flag.Parse()

	// File logging is opt-in. When enabled, the file is truncated on every
	// process start — so `make restart` / `make deploy` (which restart the
	// systemd unit) automatically yields a clean log. We also keep stderr
	// so journald continues to see the same lines for `journalctl -u`.
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			log.Fatalf("cannot open log file %q: %v", *logFile, err)
		}
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		log.Printf("[boot] log file: %s (truncated on start)", *logFile)
	}

	resProfile := creator.ParseResources(*resources, *customReadBuf, *customMaxDCBuf, *customMemLimit)

	// Load VK cookies (used for authentication and creating calls)
	cookies, err := config.LoadCookies(*cookiesPath)
	if err != nil {
		log.Printf("Warning: failed to load cookies from %s: %v", *cookiesPath, err)
		log.Println("Continuing with empty cookies for mock purposes...")
	}

	// Initialize the main VPN Creator Daemon
	vpnDaemon := daemon.NewDaemon(cookies, resProfile, *callTTL)

	// Create root context to handle graceful shutdown (Ctrl+C)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Println("Received termination signal. Shutting down...")
		cancel()
	}()

	// Start the Daemon orchestrator in the background
	go vpnDaemon.Start(ctx)

	// Start the local API server (blocks main goroutine)
	localServer := api.NewServer(*apiPort, vpnDaemon)
	if err := localServer.Start(); err != nil {
		log.Fatalf("API Server failed: %v", err)
	}
}
