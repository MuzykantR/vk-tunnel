package proxy

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/armon/go-socks5"
	"github.com/vk-vpn/client/webrtc"
)

type SOCKS5Server struct {
	port   int
	webrtc *webrtc.Client
	server *socks5.Server
}

func NewSOCKS5Server(port int, wrtcClient *webrtc.Client) (*SOCKS5Server, error) {
	// Create a SOCKS5 server
	conf := &socks5.Config{
		// We implement a custom Dialer to route traffic to WebRTC instead of the local network.
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// In a real proxy, we would tell the WebRTC tunnel to establish a stream to `addr`
			// and return a virtual `net.Conn` representing that stream.
			log.Printf("[SOCKS5] Intercepted connection request to: %s", addr)
			
			// For demonstration, we return a mock connection or fail.
			// The actual bridging logic is complex and involves a multiplexer like smux.
			return nil, fmt.Errorf("WebRTC virtual dialer not fully implemented for %s", addr)
		},
	}
	
	// Temporarily, we can just use the default dialer for testing the local SOCKS5 setup, 
	// but production code MUST use the custom dialer above to route into WebRTC.
	// conf = &socks5.Config{} 

	server, err := socks5.New(conf)
	if err != nil {
		return nil, err
	}

	return &SOCKS5Server{
		port:   port,
		webrtc: wrtcClient,
		server: server,
	}, nil
}

func (s *SOCKS5Server) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	log.Printf("Starting SOCKS5 Server on %s", addr)
	return s.server.ListenAndServe("tcp", addr)
}
