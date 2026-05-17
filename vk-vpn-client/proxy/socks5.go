package proxy

import (
	"fmt"
	"log"
	"net"

	"github.com/vk-vpn/client/webrtc"
)

type SOCKS5Server struct {
	port   int
	webrtc *webrtc.Client
}

func NewSOCKS5Server(port int, wrtcClient *webrtc.Client) (*SOCKS5Server, error) {
	return &SOCKS5Server{
		port:   port,
		webrtc: wrtcClient,
	}, nil
}

func (s *SOCKS5Server) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("Starting SOCKS5 Listener on %s. Forwarding streams to WebRTC...", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		
		// We pass the raw TCP connection to the WebRTC client.
		// Since we use a single DataChannel, the remote VPN daemon
		// will receive the raw bytes (including the SOCKS5 handshake)
		// and perform the actual routing natively on the server side.
		s.webrtc.HandleSOCKS5Conn(conn)
	}
}

