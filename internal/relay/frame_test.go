package relay

import "testing"

func TestExtractFramedMPEGTSKeepsValidPrefix(t *testing.T) {
	pkt := make([]byte, mpegTSPacketSize)
	pkt[0] = 0x47
	var data []byte
	for i := 0; i < 5; i++ {
		data = append(data, pkt...)
	}
	data = append(data, make([]byte, 200)...) // trailing padding

	framed, carry := extractFramedMPEGTS(data)
	if len(framed) != 5*mpegTSPacketSize {
		t.Fatalf("framed=%d want %d", len(framed), 5*mpegTSPacketSize)
	}
	if len(carry) != 200 {
		t.Fatalf("carry=%d want 200", len(carry))
	}
	ok, valid := framedMPEGTS(data)
	if !valid || len(ok) != 5*mpegTSPacketSize {
		t.Fatalf("framedMPEGTS valid=%v len=%d", valid, len(ok))
	}
}
