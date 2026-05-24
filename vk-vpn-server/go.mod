module github.com/vk-vpn/server

go 1.25.1

require (
	github.com/gorilla/websocket v1.5.3
	github.com/pion/datachannel v1.5.8
	github.com/pion/webrtc/v3 v3.3.6
	github.com/vk-vpn/client v0.0.0-00010101000000-000000000000
)

replace github.com/vk-vpn/client => ../vk-vpn-client

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.3.1 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/klauspost/reedsolomon v1.12.0 // indirect
	github.com/pion/dtls/v2 v2.2.12 // indirect
	github.com/pion/ice/v2 v2.3.38 // indirect
	github.com/pion/interceptor v0.1.29 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/mdns v0.0.12 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.14 // indirect
	github.com/pion/rtp v1.8.7 // indirect
	github.com/pion/sctp v1.8.19 // indirect
	github.com/pion/sdp/v3 v3.0.9 // indirect
	github.com/pion/srtp/v2 v2.0.20 // indirect
	github.com/pion/stun v0.6.1 // indirect
	github.com/pion/transport/v2 v2.2.10 // indirect
	github.com/pion/turn/v2 v2.1.6 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.9.0 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/wlynxg/anet v0.0.3 // indirect
	github.com/xtaci/kcp-go/v5 v5.6.72 // indirect
	golang.org/x/crypto v0.45.0 // indirect
	golang.org/x/net v0.47.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
