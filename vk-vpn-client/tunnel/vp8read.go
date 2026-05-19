package tunnel

import (
	"log"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
)

// ReadVP8Track reassembles VP8 RTP into obfuscated frames (whitelist-bypass).
func ReadVP8Track(track *webrtc.TrackRemote, handler func([]byte)) {
	if track.Codec().MimeType != webrtc.MimeTypeVP8 {
		buf := make([]byte, 65536)
		for {
			if _, _, err := track.Read(buf); err != nil {
				return
			}
		}
	}
	var vp8Pkt codecs.VP8Packet
	var frameBuf []byte
	var lastSeq uint16
	var haveLastSeq bool
	frameValid := false
	buf := make([]byte, 65536)
	for {
		n, _, err := track.Read(buf)
		if err != nil {
			return
		}
		pkt := &rtp.Packet{}
		if pkt.Unmarshal(buf[:n]) != nil {
			continue
		}
		if haveLastSeq && pkt.SequenceNumber != lastSeq+1 {
			frameValid = false
			frameBuf = frameBuf[:0]
		}
		lastSeq = pkt.SequenceNumber
		haveLastSeq = true
		vp8Payload, err := vp8Pkt.Unmarshal(pkt.Payload)
		if err != nil {
			frameValid = false
			frameBuf = frameBuf[:0]
			continue
		}
		if vp8Pkt.S == 1 {
			frameBuf = frameBuf[:0]
			frameValid = true
		}
		if !frameValid {
			continue
		}
		frameBuf = append(frameBuf, vp8Payload...)
		if !pkt.Marker {
			continue
		}
		if handler != nil {
			cp := make([]byte, len(frameBuf))
			copy(cp, frameBuf)
			handler(cp)
		}
		frameBuf = frameBuf[:0]
		frameValid = false
	}
}

// ReadVP8TrackLogged wraps ReadVP8Track with sparse debug logging.
func ReadVP8TrackLogged(track *webrtc.TrackRemote, handler func([]byte)) {
	n := 0
	ReadVP8Track(track, func(frame []byte) {
		n++
		if n <= 3 || n%200 == 0 {
			log.Printf("[video] recv vp8 frame #%d %d bytes", n, len(frame))
		}
		handler(frame)
	})
}
