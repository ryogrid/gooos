// src/ipv4.go -- Minimal IPv4 parser / builder + Ethernet glue.
//
// Scope: RFC 791 unfragmented v4 datagrams with IHL=5 (20-byte header,
// no options). Outbound packets skip fragmentation entirely — callers
// must keep payload ≤ 1500 - 20 bytes. Inbound packets with MF=1 or
// fragment offset ≠ 0, bad version/IHL, TTL=0, or a checksum mismatch
// are silently dropped.
//
// The on-wire layout is accessed byte-by-byte — no Go struct casts.

package main

const (
	ipv4HeaderMinSize = 20
	ipv4DefaultTTL    = uint8(64)
	ipv4MTU           = 1500 // Ethernet MTU
	ipv4MaxPayload    = ipv4MTU - ipv4HeaderMinSize

	ipProtoICMP = uint8(1)
	ipProtoTCP  = uint8(6)
	ipProtoUDP  = uint8(17)

	ipv4FlagDF = uint16(0x4000) // Don't Fragment
	ipv4FlagMF = uint16(0x2000) // More Fragments
	ipv4FragMask = uint16(0x1FFF)
)

// IPv4Header is the host-order view of a parsed datagram header.
type IPv4Header struct {
	VersionIHL  uint8
	TOS         uint8
	TotalLength uint16
	ID          uint16
	FlagsOffset uint16 // combined: flags in top 3 bits, offset in low 13
	TTL         uint8
	Protocol    uint8
	Checksum    uint16
	SrcIP       uint32
	DstIP       uint32
	HeaderLen   int // IHL * 4
}

// Monotonic packet ID for outbound headers.
var ipv4ID uint16

// ipv4Checksum computes the 16-bit ones-complement of the ones-
// complement sum of 16-bit words in `data`. Works for both header
// validation (pass the full header, expect 0 / 0xFFFF) and new-header
// generation (set checksum field to 0, compute, write result).
//
// Handles odd lengths by padding the final byte with a zero high half.
func ipv4Checksum(data []byte) uint16 {
	sum := uint32(0)
	i := 0
	for ; i+1 < len(data); i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if i < len(data) {
		sum += uint32(data[i]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ipv4Parse decodes the first 20+ bytes of `payload` into an
// IPv4Header. Returns the header, the inner payload slice, and ok=true
// only when the frame survives every validation (version, IHL bounds,
// fragmentation rejection, checksum).
func ipv4Parse(payload []byte) (IPv4Header, []byte, bool) {
	if len(payload) < ipv4HeaderMinSize {
		return IPv4Header{}, nil, false
	}
	var h IPv4Header
	h.VersionIHL = payload[0]
	version := h.VersionIHL >> 4
	ihl := int(h.VersionIHL & 0x0F)
	h.HeaderLen = ihl * 4
	if version != 4 || ihl < 5 || h.HeaderLen > len(payload) {
		return h, nil, false
	}

	h.TOS = payload[1]
	h.TotalLength = uint16(payload[2])<<8 | uint16(payload[3])
	h.ID = uint16(payload[4])<<8 | uint16(payload[5])
	h.FlagsOffset = uint16(payload[6])<<8 | uint16(payload[7])
	h.TTL = payload[8]
	h.Protocol = payload[9]
	h.Checksum = uint16(payload[10])<<8 | uint16(payload[11])
	h.SrcIP = uint32(payload[12])<<24 | uint32(payload[13])<<16 |
		uint32(payload[14])<<8 | uint32(payload[15])
	h.DstIP = uint32(payload[16])<<24 | uint32(payload[17])<<16 |
		uint32(payload[18])<<8 | uint32(payload[19])

	// Reject fragments (MF set or non-zero offset).
	if h.FlagsOffset&(ipv4FlagMF|ipv4FragMask) != 0 {
		statsInc(&netStats.FragmentsDropped)
		return h, nil, false
	}
	// Reject obvious junk.
	if h.TTL == 0 {
		return h, nil, false
	}
	if int(h.TotalLength) > len(payload) || int(h.TotalLength) < h.HeaderLen {
		return h, nil, false
	}
	// Validate checksum over the header (bytes 0..HeaderLen).
	if ipv4Checksum(payload[:h.HeaderLen]) != 0 {
		statsInc(&netStats.ChecksumErr)
		return h, nil, false
	}

	inner := payload[h.HeaderLen:int(h.TotalLength)]
	return h, inner, true
}

// ipv4BuildHeader writes a fresh 20-byte header (IHL=5, DF set, TTL=64)
// into the first 20 bytes of `buf`. Caller is responsible for any
// protocol-specific checksum in the payload.
func ipv4BuildHeader(buf []byte, proto uint8, srcIP, dstIP uint32, payloadLen int) {
	total := uint16(ipv4HeaderMinSize + payloadLen)
	ipv4ID++
	id := ipv4ID

	buf[0] = 0x45 // version 4, IHL 5
	buf[1] = 0
	buf[2] = byte(total >> 8)
	buf[3] = byte(total & 0xFF)
	buf[4] = byte(id >> 8)
	buf[5] = byte(id & 0xFF)
	buf[6] = byte(ipv4FlagDF >> 8) // flags: DF, offset = 0
	buf[7] = 0
	buf[8] = ipv4DefaultTTL
	buf[9] = proto
	buf[10] = 0 // checksum placeholder
	buf[11] = 0
	buf[12] = byte(srcIP >> 24)
	buf[13] = byte(srcIP >> 16)
	buf[14] = byte(srcIP >> 8)
	buf[15] = byte(srcIP & 0xFF)
	buf[16] = byte(dstIP >> 24)
	buf[17] = byte(dstIP >> 16)
	buf[18] = byte(dstIP >> 8)
	buf[19] = byte(dstIP & 0xFF)

	csum := ipv4Checksum(buf[:ipv4HeaderMinSize])
	buf[10] = byte(csum >> 8)
	buf[11] = byte(csum & 0xFF)
}

// ipv4Send resolves the next-hop MAC via ARP, prepends IP + Ethernet
// headers around `payload`, and transmits. Returns false on any
// failure (payload too large, ARP timeout, TX ring full).
func ipv4Send(proto uint8, dstIP uint32, payload []byte) bool {
	if !e1000Found || ourIP == 0 {
		return false
	}
	if len(payload) > ipv4MaxPayload {
		return false
	}
	nh := nextHopIP(dstIP)
	mac, ok := arpResolve(nh)
	if !ok {
		return false
	}

	frame := make([]byte, ethernetHeaderSize+ipv4HeaderMinSize+len(payload))
	// Ethernet header
	copy(frame[0:6], mac[:])
	copy(frame[6:12], e1000MAC[:])
	frame[12] = byte(etherTypeIPv4 >> 8)
	frame[13] = byte(etherTypeIPv4 & 0xFF)
	// IP header
	ipv4BuildHeader(frame[ethernetHeaderSize:], proto, ourIP, dstIP, len(payload))
	// Payload
	copy(frame[ethernetHeaderSize+ipv4HeaderMinSize:], payload)

	return e1000Transmit(frame)
}

// ipv4Handle is called by ethernetDispatch for each RX IPv4 frame.
// Parses and validates the header, then dispatches on Protocol.
// Unknown protocols are dropped silently.
func ipv4Handle(payload []byte) {
	hdr, inner, ok := ipv4Parse(payload)
	if !ok {
		return
	}
	// Accept packets addressed to our IP or to the all-ones
	// limited broadcast (255.255.255.255). DHCP replies use the
	// limited broadcast when the client sets the BOOTP flags.B bit,
	// which is the only way an unconfigured client can receive the
	// server's OFFER before it has an IP of its own.
	// Subnet-directed broadcast (e.g. 10.0.2.255) is also accepted
	// when netmask and ourIP are set; otherwise dropped.
	if hdr.DstIP != ourIP && hdr.DstIP != 0xFFFFFFFF {
		if ourNetmask == 0 || ourIP == 0 {
			return
		}
		subnetBcast := (ourIP & ourNetmask) | ^ourNetmask
		if hdr.DstIP != subnetBcast {
			return
		}
	}
	switch hdr.Protocol {
	case ipProtoICMP:
		icmpHandle(hdr, inner)
	case ipProtoTCP:
		tcpHandle(hdr, inner)
	case ipProtoUDP:
		udpHandle(hdr, inner)
	default:
		// Unknown protocol — drop.
	}
}
