package mpegts

const (
	PacketSize = 188
	SyncByte   = 0x47
)

// ExtractFramed returns the longest leading run of complete 188-byte
// packets that start with a sync byte, plus any remaining trailing bytes.
func ExtractFramed(data []byte) (framed, carry []byte) {
	start := -1
	for i := 0; i < len(data); i++ {
		if data[i] == SyncByte {
			start = i
			break
		}
	}
	if start < 0 {
		if len(data) > PacketSize-1 {
			return nil, append([]byte(nil), data[len(data)-(PacketSize-1):]...)
		}
		return nil, append([]byte(nil), data...)
	}
	end := start
	for end+PacketSize <= len(data) {
		if data[end] != SyncByte {
			break
		}
		end += PacketSize
	}
	if end == start {
		return nil, append([]byte(nil), data[start:]...)
	}
	return data[start:end], append([]byte(nil), data[end:]...)
}

// Framed validates a complete buffer as MPEG-TS and returns aligned packets.
// Trailing padding after a valid TS prefix is discarded.
func Framed(data []byte) ([]byte, bool) {
	framed, _ := ExtractFramed(data)
	if len(framed) < PacketSize {
		return nil, false
	}
	return framed, true
}

// PID returns the 13-bit packet identifier from a TS header.
func PID(pkt []byte) uint16 {
	if len(pkt) < 3 {
		return 0
	}
	return uint16(pkt[1]&0x1f)<<8 | uint16(pkt[2])
}

// HasPUSI reports whether the payload unit start indicator is set.
func HasPUSI(pkt []byte) bool {
	return len(pkt) > 1 && pkt[1]&0x40 != 0
}

// HasRandomAccess reports the MPEG-TS adaptation-field random_access_indicator.
// ffmpeg's mpegts muxer sets this on video keyframes.
func HasRandomAccess(pkt []byte) bool {
	if len(pkt) < 6 || pkt[0] != SyncByte {
		return false
	}
	afc := (pkt[3] >> 4) & 0x3
	if afc != 2 && afc != 3 {
		return false
	}
	afLen := int(pkt[4])
	if afLen < 1 || 5+afLen > len(pkt) {
		return false
	}
	return pkt[5]&0x40 != 0
}
