// src/icmp.go -- Minimal ICMPv4 handler: echo reply only.
//
// RFC 792, Type 8 (Echo Request) / Type 0 (Echo Reply). All other
// ICMP types are dropped silently; we do not emit Destination
// Unreachable / Time Exceeded / etc.

package main

const (
	icmpTypeEchoReply   = uint8(0)
	icmpTypeEchoRequest = uint8(8)
	icmpHeaderSize      = 8
)

// icmpHandle is invoked by ipv4Handle when Protocol == 1. `inner` is
// the ICMP header + payload (no IP header in front).
func icmpHandle(hdr IPv4Header, inner []byte) {
	if len(inner) < icmpHeaderSize {
		return
	}
	if inner[0] != icmpTypeEchoRequest {
		return
	}
	// Validate the request checksum. ICMP uses the same ones-complement
	// algorithm as the IP header (`ipv4Checksum`).
	if ipv4Checksum(inner) != 0 {
		return
	}

	reply := make([]byte, len(inner))
	copy(reply, inner)
	reply[0] = icmpTypeEchoReply
	reply[1] = 0
	// Zero the checksum field before recomputing.
	reply[2] = 0
	reply[3] = 0
	csum := ipv4Checksum(reply)
	reply[2] = byte(csum >> 8)
	reply[3] = byte(csum & 0xFF)

	ipv4Send(ipProtoICMP, hdr.SrcIP, reply)
}

// testICMPEchoReply synthesizes an ICMP echo-request addressed to
// ourIP, feeds it through ipv4Handle, and checks that e1000's TX tail
// advanced — i.e., icmpHandle ran and emitted a reply. Pre-seeds the
// ARP cache so arpResolve doesn't block. Runs once at boot; prints
// PASS/FAIL to serial.
func testICMPEchoReply() {
	if !e1000Found || ourIP == 0 {
		return
	}
	// Pre-populate ARP so the reply's nextHop is resolved instantly.
	arpLearn(ourGateway, [6]byte{0x52, 0x55, 0x0A, 0x00, 0x02, 0x02})

	// Build a synthetic IP + ICMP datagram: ICMP echo request with
	// identifier / sequence / data all zero.
	const payloadLen = icmpHeaderSize + 4
	pkt := make([]byte, ipv4HeaderMinSize+payloadLen)

	// Reuse ipv4BuildHeader to get a valid IP header (src = gateway,
	// dst = ourIP).
	ipv4BuildHeader(pkt, ipProtoICMP, ourGateway, ourIP, payloadLen)

	icmp := pkt[ipv4HeaderMinSize:]
	icmp[0] = icmpTypeEchoRequest
	icmp[1] = 0
	icmp[2] = 0
	icmp[3] = 0
	icmp[4] = 0x12
	icmp[5] = 0x34 // identifier
	icmp[6] = 0
	icmp[7] = 1 // sequence
	icmp[8] = 0xDE
	icmp[9] = 0xAD
	icmp[10] = 0xBE
	icmp[11] = 0xEF
	csum := ipv4Checksum(icmp)
	icmp[2] = byte(csum >> 8)
	icmp[3] = byte(csum & 0xFF)

	before := txTail
	ipv4Handle(pkt)
	after := txTail

	if after != before {
		serialPrintln("TEST: icmp echo reply PASS")
	} else {
		serialPrintln("TEST: icmp echo reply FAIL")
	}
}
