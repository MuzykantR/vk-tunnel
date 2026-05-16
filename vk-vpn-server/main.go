package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/vk-vpn/server/api"
	"github.com/vk-vpn/server/config"
	"github.com/vk-vpn/server/daemon"
)

func main() {
	cookiesPath := flag.String("cookies", "cookies.json", "Path to cookies.json file")
	apiPort := flag.Int("port", 8080, "Local API port for the Bot")
	flag.Parse()

	// Load VK cookies (used for authentication and creating calls)
	cookies, err := config.LoadCookies(*cookiesPath)
	if err != nil {
		log.Printf("Warning: failed to load cookies from %s: %v", *cookiesPath, err)
		log.Println("Continuing with empty cookies for mock purposes...")
	}

	// Initialize the main VPN Creator Daemon
	vpnDaemon := daemon.NewDaemon(cookies)

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
