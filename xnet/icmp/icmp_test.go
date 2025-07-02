package icmp

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/ipv4"
)

// manualChecksum independently calculates the ICMP checksum.
func manualChecksum(b []byte) uint16 {
	var sum uint32
	for len(b) > 1 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) > 0 {
		sum += uint32(b[0]) << 8
	}
	for (sum >> 16) > 0 {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func TestMessageMarshalChecksum(t *testing.T) {
	msg := &Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &Echo{ID: 0x1234, Seq: 1, Data: []byte("Hello")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	bZero := append([]byte(nil), b...)
	binary.BigEndian.PutUint16(bZero[2:4], 0)
	want := manualChecksum(bZero)
	got := binary.BigEndian.Uint16(b[2:4])
	if got != want {
		t.Errorf("checksum mismatch: got 0x%x, want 0x%x", got, want)
	}
}
