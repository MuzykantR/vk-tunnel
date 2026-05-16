package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/vk-vpn/server/daemon"
)

type Server struct {
	port   int
	daemon *daemon.Daemon
}

func NewServer(port int, d *daemon.Daemon) *Server {
	return &Server{
		port:   port,
		daemon: d,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/get_link", s.handleGetLink)

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	log.Printf("Starting local API server on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleGetLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	link, pubKey := s.daemon.GetLinkInfo()

	response := map[string]string{
		"link":      link,
		"server_pk": pubKey,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
