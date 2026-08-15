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

func TestHasRandomAccess(t *testing.T) {
	payloadOnly := make([]byte, PacketSize)
	payloadOnly[0] = SyncByte
	payloadOnly[3] = 0x10
	if HasRandomAccess(payloadOnly) {
		t.Fatal("payload-only packet must not report RAI")
	}

	rai := make([]byte, PacketSize)
	rai[0] = SyncByte
	rai[3] = 0x30 // adaptation + payload
	rai[4] = 1
	rai[5] = 0x40 // random_access_indicator
	if !HasRandomAccess(rai) {
		t.Fatal("expected RAI")
	}

	emptyAF := make([]byte, PacketSize)
	emptyAF[0] = SyncByte
	emptyAF[3] = 0x30
	emptyAF[4] = 0
	if HasRandomAccess(emptyAF) {
		t.Fatal("zero-length adaptation field has no RAI")
	}
}
