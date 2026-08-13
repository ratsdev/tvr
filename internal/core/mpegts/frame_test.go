package mpegts

import "testing"

func TestExtractFramedKeepsValidPrefix(t *testing.T) {
	pkt := make([]byte, PacketSize)
	pkt[0] = SyncByte
	var data []byte
	for i := 0; i < 5; i++ {
		data = append(data, pkt...)
	}
	data = append(data, make([]byte, 200)...)

	framed, carry := ExtractFramed(data)
	if len(framed) != 5*PacketSize {
		t.Fatalf("framed=%d want %d", len(framed), 5*PacketSize)
	}
	if len(carry) != 200 {
		t.Fatalf("carry=%d want 200", len(carry))
	}
	ok, valid := Framed(data)
	if !valid || len(ok) != 5*PacketSize {
		t.Fatalf("Framed valid=%v len=%d", valid, len(ok))
	}
}
