package tunnel

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
)

// Reassembler counters — exported atomics so Phase 2 benchmark can correlate
// "curl partial download" with the rate of RTP-gap-induced frame drops. A
// non-zero reset counter at 100% curl completion means we got lucky; any
// reset under load is a confirmed app-level byte loss event.
var (
	vp8RTPGapResets   atomic.Uint64 // RTP sequence number gap → drop current frame
	vp8DepacketErrors atomic.Uint64 // pion VP8 depacketizer rejected payload
	vp8FramesEmitted  atomic.Uint64 // VP8 samples delivered to handler
)

// VP8ReassemblerStats snapshots the diagnostic counters. Useful for the
// Phase 2 benchmark scripts.
type VP8ReassemblerStats struct {
	GapResets       uint64
	DepacketErrors  uint64
	FramesEmitted   uint64
}

func ReadVP8ReassemblerStats() VP8ReassemblerStats {
	return VP8ReassemblerStats{
		GapResets:      vp8RTPGapResets.Load(),
		DepacketErrors: vp8DepacketErrors.Load(),
		FramesEmitted:  vp8FramesEmitted.Load(),
	}
}

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

	// Periodic log so a long-running session can see whether the gap rate
	// is climbing without grepping per-packet logs. Throttled to once per
	// 30s and only printed when something actually happened.
	var (
		lastLog      time.Time
		lastGap      uint64
		lastDepacket uint64
		lastEmitted  uint64
	)

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
			vp8RTPGapResets.Add(1)
			frameValid = false
			frameBuf = frameBuf[:0]
		}
		lastSeq = pkt.SequenceNumber
		haveLastSeq = true
		vp8Payload, err := vp8Pkt.Unmarshal(pkt.Payload)
		if err != nil {
			vp8DepacketErrors.Add(1)
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
		vp8FramesEmitted.Add(1)
		frameBuf = frameBuf[:0]
		frameValid = false

		now := time.Now()
		if now.Sub(lastLog) >= 30*time.Second {
			gap := vp8RTPGapResets.Load()
			depacket := vp8DepacketErrors.Load()
			emitted := vp8FramesEmitted.Load()
			dGap := gap - lastGap
			dDepacket := depacket - lastDepacket
			dEmitted := emitted - lastEmitted
			if dGap > 0 || dDepacket > 0 {
				log.Printf("[vp8-reasm] interval frames=%d gap_resets=%d depacket_errs=%d (cumulative frames=%d gaps=%d depacket=%d)",
					dEmitted, dGap, dDepacket, emitted, gap, depacket)
			}
			lastLog = now
			lastGap = gap
			lastDepacket = depacket
			lastEmitted = emitted
		}
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
