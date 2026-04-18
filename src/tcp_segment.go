package main

// TCP segment wire format — parse/build, pseudo-header
// checksum, and the MSS option (the only option v1 emits on
// SYN). Design: impldoc/net_tcp_segment_io.md.

const (
	tcpHeaderMinSize = 20
	tcpHeaderMaxSize = 60 // 20 B fixed + up to 40 B options

	tcpFlagFIN = uint8(0x01)
	tcpFlagSYN = uint8(0x02)
	tcpFlagRST = uint8(0x04)
	tcpFlagPSH = uint8(0x08)
	tcpFlagACK = uint8(0x10)
	tcpFlagURG = uint8(0x20)

	tcpOptEnd    = uint8(0)
	tcpOptNop    = uint8(1)
	tcpOptMSS    = uint8(2)
	tcpOptWScale = uint8(3)
	tcpOptSACKok = uint8(4)
	tcpOptSACK   = uint8(5)
	tcpOptTS     = uint8(8)

	tcpDefaultMSS = uint16(536) // RFC 1122 floor
)

// TCPHeader is the host-order view of a parsed segment header.
// Options bytes are captured verbatim; tcpParseOptions walks
// them if present.
type TCPHeader struct {
	SrcPort uint16
	DstPort uint16
	Seq     uint32
	Ack     uint32
	DataOff uint8 // 4 bits; header length = DataOff*4 bytes
	Flags   uint8 // FIN|SYN|RST|PSH|ACK|URG (bits 0..5)
	Window  uint16
	Chksum  uint16
	Urgent  uint16
	Options [40]byte
	OptLen  uint8
}

// tcpParse splits a TCP segment into (header, payload). ok=false
// on truncated header, DataOff<5, hdrLen exceeding packet length,
// or hdrLen exceeding the 60-byte maximum. Checksum is NOT
// verified here — call tcpChecksumVerify separately.
func tcpParse(packet []byte) (TCPHeader, []byte, bool) {
	if len(packet) < tcpHeaderMinSize {
		return TCPHeader{}, nil, false
	}
	h := TCPHeader{
		SrcPort: uint16(packet[0])<<8 | uint16(packet[1]),
		DstPort: uint16(packet[2])<<8 | uint16(packet[3]),
		Seq: uint32(packet[4])<<24 | uint32(packet[5])<<16 |
			uint32(packet[6])<<8 | uint32(packet[7]),
		Ack: uint32(packet[8])<<24 | uint32(packet[9])<<16 |
			uint32(packet[10])<<8 | uint32(packet[11]),
		DataOff: packet[12] >> 4,
		Flags:   packet[13] & 0x3F,
		Window:  uint16(packet[14])<<8 | uint16(packet[15]),
		Chksum:  uint16(packet[16])<<8 | uint16(packet[17]),
		Urgent:  uint16(packet[18])<<8 | uint16(packet[19]),
	}
	if h.DataOff < 5 {
		return h, nil, false
	}
	hdrLen := int(h.DataOff) * 4
	if hdrLen > tcpHeaderMaxSize || hdrLen > len(packet) {
		return h, nil, false
	}
	optLen := hdrLen - tcpHeaderMinSize
	if optLen > 0 {
		copy(h.Options[:optLen], packet[tcpHeaderMinSize:hdrLen])
		h.OptLen = uint8(optLen)
	}
	return h, packet[hdrLen:], true
}

// tcpBuildSegment composes a TCP segment (header + payload) into
// out. The Chksum field is left zero; fill it via
// tcpComputeAndSetChecksum after calling this. `options` must be
// pre-padded to a 4-byte boundary and at most 40 bytes; v1 only
// ever passes 4 bytes (MSS option) or nil. Returns total bytes
// written, or 0 on invalid input.
func tcpBuildSegment(out []byte,
	srcPort, dstPort uint16,
	seq, ack uint32,
	flags uint8,
	window uint16,
	options []byte,
	payload []byte) int {

	optLen := len(options)
	if optLen%4 != 0 || optLen > 40 {
		return 0
	}
	hdrLen := tcpHeaderMinSize + optLen
	total := hdrLen + len(payload)
	if len(out) < total {
		return 0
	}
	out[0] = byte(srcPort >> 8)
	out[1] = byte(srcPort)
	out[2] = byte(dstPort >> 8)
	out[3] = byte(dstPort)
	out[4] = byte(seq >> 24)
	out[5] = byte(seq >> 16)
	out[6] = byte(seq >> 8)
	out[7] = byte(seq)
	out[8] = byte(ack >> 24)
	out[9] = byte(ack >> 16)
	out[10] = byte(ack >> 8)
	out[11] = byte(ack)
	out[12] = byte((hdrLen / 4) << 4)
	out[13] = flags & 0x3F
	out[14] = byte(window >> 8)
	out[15] = byte(window)
	out[16] = 0 // Chksum placeholder
	out[17] = 0
	out[18] = 0 // Urgent
	out[19] = 0
	if optLen > 0 {
		copy(out[20:20+optLen], options)
	}
	copy(out[hdrLen:], payload)
	return total
}

// tcpParseOptions walks the 0..40 option bytes and extracts the
// MSS advertised by the peer. Unknown options are skipped.
// Malformed TLVs (length<2 or running off the end) cause early
// return with peerMSS=tcpDefaultMSS and ok=false. The iteration
// cap of 40 (one pass per option byte, worst case) guarantees
// termination even if the length byte is fabricated.
func tcpParseOptions(opts []byte) (peerMSS uint16, ok bool) {
	peerMSS = tcpDefaultMSS
	i := 0
	for iter := 0; iter < 40 && i < len(opts); iter++ {
		kind := opts[i]
		switch kind {
		case tcpOptEnd:
			return peerMSS, true
		case tcpOptNop:
			i++
			continue
		}
		if i+1 >= len(opts) {
			return tcpDefaultMSS, false
		}
		length := int(opts[i+1])
		if length < 2 || i+length > len(opts) {
			return tcpDefaultMSS, false
		}
		if kind == tcpOptMSS && length == 4 {
			peerMSS = uint16(opts[i+2])<<8 | uint16(opts[i+3])
		}
		i += length
	}
	return peerMSS, true
}

// tcpBuildMSSOption fills out[0:4] with an MSS option carrying
// mss. Returns 4. v1 emits this only on SYN and SYN|ACK.
func tcpBuildMSSOption(out []byte, mss uint16) int {
	out[0] = tcpOptMSS
	out[1] = 4
	out[2] = byte(mss >> 8)
	out[3] = byte(mss)
	return 4
}

// tcpChecksum computes the RFC 793 checksum over the pseudo-
// header + tcpPacket (the TCP header with Chksum=0 plus payload).
// Mirrors src/udp.go:69-104 (udpChecksum). Unlike UDP, TCP has no
// "0 means disabled" convention — zero is a legal on-wire value
// that means "the ones-complement sum was all ones" and must be
// transmitted verbatim.
func tcpChecksum(srcIP, dstIP uint32, tcpPacket []byte) uint16 {
	var pseudo [12]byte
	pseudo[0] = byte(srcIP >> 24)
	pseudo[1] = byte(srcIP >> 16)
	pseudo[2] = byte(srcIP >> 8)
	pseudo[3] = byte(srcIP)
	pseudo[4] = byte(dstIP >> 24)
	pseudo[5] = byte(dstIP >> 16)
	pseudo[6] = byte(dstIP >> 8)
	pseudo[7] = byte(dstIP)
	pseudo[8] = 0
	pseudo[9] = ipProtoTCP
	l := uint16(len(tcpPacket))
	pseudo[10] = byte(l >> 8)
	pseudo[11] = byte(l)

	sum := uint32(0)
	for i := 0; i+1 < len(pseudo); i += 2 {
		sum += uint32(pseudo[i])<<8 | uint32(pseudo[i+1])
	}
	i := 0
	for ; i+1 < len(tcpPacket); i += 2 {
		sum += uint32(tcpPacket[i])<<8 | uint32(tcpPacket[i+1])
	}
	if i < len(tcpPacket) {
		sum += uint32(tcpPacket[i]) << 8 // odd byte padded
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// tcpChecksumVerify recomputes the checksum over the segment
// and compares it to the on-wire value. Unlike UDP, a zero
// checksum is NOT a hint to skip verification — recompute and
// compare either way.
func tcpChecksumVerify(srcIP, dstIP uint32, packet []byte) bool {
	if len(packet) < tcpHeaderMinSize {
		return false
	}
	wire := uint16(packet[16])<<8 | uint16(packet[17])
	scratch := make([]byte, len(packet))
	copy(scratch, packet)
	scratch[16] = 0
	scratch[17] = 0
	return tcpChecksum(srcIP, dstIP, scratch) == wire
}

// tcpComputeAndSetChecksum fills the Chksum field of a segment
// built via tcpBuildSegment. The header must already have
// Chksum=0 (which tcpBuildSegment guarantees).
func tcpComputeAndSetChecksum(srcIP, dstIP uint32, packet []byte) {
	c := tcpChecksum(srcIP, dstIP, packet)
	packet[16] = byte(c >> 8)
	packet[17] = byte(c)
}
