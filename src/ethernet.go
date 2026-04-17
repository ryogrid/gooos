// src/ethernet.go -- Ethernet II framing parse/build + address helpers.
//
// Frames are manipulated at the byte level (no struct casting) so the
// layout matches the wire regardless of Go / TinyGo struct padding.
//
// The higher-level RX dispatch (switch on EtherType and hand off to
// the ARP or IPv4 handler) lives in src/net.go.

package main

const (
	ethernetHeaderSize = 14
	ethernetMinFrame   = 14 // Our floor; QEMU's RX strips CRC so 14 is the absolute minimum.
	ethernetMaxFrame   = 1518

	etherTypeIPv4 = uint16(0x0800)
	etherTypeARP  = uint16(0x0806)
)

// broadcastMAC is the all-ones destination address for broadcast frames.
var broadcastMAC = [6]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// zeroMAC is the all-zero sentinel, useful for un-resolved ARP targets.
var zeroMAC = [6]byte{0, 0, 0, 0, 0, 0}

// EthernetHeader mirrors the 14-byte on-wire Ethernet II header. Callers
// should not depend on Go's struct layout — use ethernetParse to extract
// fields and ethernetBuild to emit a frame.
type EthernetHeader struct {
	Dst       [6]byte
	Src       [6]byte
	EtherType uint16 // host byte order
}

// ethernetParse decodes the first 14 bytes of `frame` into an
// EthernetHeader and returns the payload slice (pointing into the same
// backing array — callers must not retain the payload past the frame's
// lifetime if the frame buffer will be reused).
//
// Returns ok=false when the frame is too short to hold a header.
func ethernetParse(frame []byte) (EthernetHeader, []byte, bool) {
	if len(frame) < ethernetHeaderSize {
		return EthernetHeader{}, nil, false
	}
	var h EthernetHeader
	copy(h.Dst[:], frame[0:6])
	copy(h.Src[:], frame[6:12])
	h.EtherType = uint16(frame[12])<<8 | uint16(frame[13])
	return h, frame[ethernetHeaderSize:], true
}

// ethernetBuild allocates a new frame with the given header fields and
// payload appended. The returned slice has length 14+len(payload).
func ethernetBuild(dst, src [6]byte, etherType uint16, payload []byte) []byte {
	out := make([]byte, ethernetHeaderSize+len(payload))
	copy(out[0:6], dst[:])
	copy(out[6:12], src[:])
	out[12] = byte(etherType >> 8)
	out[13] = byte(etherType)
	copy(out[ethernetHeaderSize:], payload)
	return out
}

// isForUs returns true when the destination MAC matches either the NIC's
// station address or the broadcast address.
func isForUs(dst [6]byte) bool {
	if dst == broadcastMAC {
		return true
	}
	return dst == e1000MAC
}
