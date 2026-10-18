package main

// Minimal MPEG-TS inspection used to keep a "GOP cache": the TS packets since
// the most recent video keyframe, plus the latest PAT/PMT. When the live
// ffmpeg is (re)started it is fed this cache first, so it can start
// immediately with a keyframe instead of waiting for the phone's next one.
// That keeps the stall on a fallback -> live switch as short as possible.

const tsPacketSize = 188

const (
	streamTypeH264 = 0x1B
	streamTypeHEVC = 0x24
)

const (
	codecH264 = "h264"
	codecHEVC = "hevc"
)

type gopCache struct {
	maxBytes int

	pmtPID    int
	videoPID  int
	videoType byte

	pat []byte
	pmt []byte
	gop []byte // whole TS packets since the last keyframe; nil = no keyframe seen yet
}

func newGOPCache(maxBytes int) *gopCache {
	return &gopCache{maxBytes: maxBytes, pmtPID: -1, videoPID: -1}
}

// push inspects one 188-byte TS packet and updates the cache.
func (c *gopCache) push(pkt []byte) {
	if len(pkt) != tsPacketSize || pkt[0] != 0x47 {
		return
	}
	pusi := pkt[1]&0x40 != 0
	pid := int(pkt[1]&0x1f)<<8 | int(pkt[2])
	afc := (pkt[3] >> 4) & 0x3

	payload := 4
	rai := false
	if afc&0x2 != 0 {
		afLen := int(pkt[4])
		if afLen > 0 {
			rai = pkt[5]&0x40 != 0
		}
		payload = 5 + afLen
	}
	hasPayload := afc&0x1 != 0 && payload < tsPacketSize

	switch {
	case pid == 0 && pusi && hasPayload:
		if p := parsePAT(pkt[payload:]); p >= 0 {
			c.pat = append(c.pat[:0], pkt...)
			if p != c.pmtPID {
				c.pmtPID, c.videoPID = p, -1
				c.gop = nil
			}
		}
	case pid == c.pmtPID && pusi && hasPayload:
		if vpid, vtype, ok := parsePMT(pkt[payload:]); ok {
			c.pmt = append(c.pmt[:0], pkt...)
			if vpid != c.videoPID {
				c.videoPID, c.videoType = vpid, vtype
				c.gop = nil
			}
		}
	case pid == c.videoPID && pusi && hasPayload:
		if rai || isKeyframePES(pkt[payload:], c.videoType) {
			if c.gop == nil {
				c.gop = make([]byte, 0, 1<<20)
			}
			c.gop = c.gop[:0]
		}
	}

	if c.gop == nil {
		return
	}
	if len(c.gop)+tsPacketSize > c.maxBytes {
		// No keyframe for far too long; keyframe detection is not working for
		// this stream. Disable the cache until the next detected keyframe.
		c.gop = nil
		return
	}
	c.gop = append(c.gop, pkt...)
}

// videoCodec returns the codec of the video stream announced in the PMT, or ""
// if no PMT has been seen yet.
func (c *gopCache) videoCodec() string {
	switch c.videoType {
	case streamTypeH264:
		return codecH264
	case streamTypeHEVC:
		return codecHEVC
	}
	return ""
}

// snapshot returns PAT + PMT + GOP as one buffer, or nil if nothing usable.
func (c *gopCache) snapshot() []byte {
	if len(c.gop) == 0 || c.pat == nil || c.pmt == nil {
		return nil
	}
	out := make([]byte, 0, len(c.pat)+len(c.pmt)+len(c.gop))
	out = append(out, c.pat...)
	out = append(out, c.pmt...)
	return append(out, c.gop...)
}

// parsePAT returns the PMT PID of the first program, or -1.
func parsePAT(p []byte) int {
	sec, ok := psiSection(p, 0x00)
	if !ok {
		return -1
	}
	// 8 bytes of header after table_id..section_length, 4 bytes CRC at the end.
	for i := 8; i+4 <= len(sec)-4; i += 4 {
		program := int(sec[i])<<8 | int(sec[i+1])
		if program != 0 {
			return int(sec[i+2]&0x1f)<<8 | int(sec[i+3])
		}
	}
	return -1
}

// parsePMT returns the PID and stream type of the first video stream.
func parsePMT(p []byte) (int, byte, bool) {
	sec, ok := psiSection(p, 0x02)
	if !ok || len(sec) < 12 {
		return 0, 0, false
	}
	progInfoLen := int(sec[10]&0x0f)<<8 | int(sec[11])
	end := len(sec) - 4 // CRC
	for i := 12 + progInfoLen; i+5 <= end; {
		st := sec[i]
		pid := int(sec[i+1]&0x1f)<<8 | int(sec[i+2])
		esInfoLen := int(sec[i+3]&0x0f)<<8 | int(sec[i+4])
		if st == streamTypeH264 || st == streamTypeHEVC {
			return pid, st, true
		}
		i += 5 + esInfoLen
	}
	return 0, 0, false
}

// psiSection extracts a PSI section (from table_id up to and including the
// CRC) that is fully contained in this packet payload.
func psiSection(p []byte, tableID byte) ([]byte, bool) {
	if len(p) < 1 {
		return nil, false
	}
	start := 1 + int(p[0]) // pointer_field
	if start+3 > len(p) || p[start] != tableID {
		return nil, false
	}
	secLen := int(p[start+1]&0x0f)<<8 | int(p[start+2])
	end := start + 3 + secLen
	if end > len(p) {
		return nil, false
	}
	return p[start:end], true
}

// isKeyframePES looks at the start of a video PES (the first TS packet only)
// for an IDR/IRAP or parameter-set NAL unit.
func isKeyframePES(p []byte, streamType byte) bool {
	if len(p) < 9 || p[0] != 0 || p[1] != 0 || p[2] != 1 {
		return false
	}
	es := 9 + int(p[8])
	if es >= len(p) {
		return false
	}
	b := p[es:]
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		h := b[i+3]
		if streamType == streamTypeHEVC {
			t := (h >> 1) & 0x3f
			if (t >= 16 && t <= 21) || t == 32 || t == 33 {
				return true
			}
		} else {
			t := h & 0x1f
			if t == 5 || t == 7 {
				return true
			}
		}
		i += 2
	}
	return false
}
