// src/udp.go -- User Datagram Protocol (RFC 768) over IPv4.
//
// Exposes a kernel-internal bind table (8 entries) so in-kernel
// services can receive UDP traffic through a Go channel. The Phase 5
// socket syscall API will layer on top of this; for now the only
// consumer is `udpEchoServer`, a tiny built-in echo service on port 7.
//
// Checksums are computed over the pseudo-header + UDP header + data.
// RFC 768 permits sending a zero checksum on IPv4 (disables validation
// on RX); we always compute on TX and translate a zero result to
// 0xFFFF as required.

package main

import "runtime"

const (
	udpHeaderSize = 8
	udpMaxBinds   = 8
)

// UDPHeader is the host-order view of the 8-byte UDP wire header.
type UDPHeader struct {
	SrcPort, DstPort uint16
	Length           uint16
	Chksum           uint16
}

// UDPDatagram is a message delivered to a bound listener.
type UDPDatagram struct {
	SrcIP   uint32
	SrcPort uint16
	DstPort uint16
	Data    []byte
}

// UDPBinding associates a port with a receive channel.
type UDPBinding struct {
	Port   uint16
	Ch     chan UDPDatagram
	Active bool
}

var (
	udpBindings [udpMaxBinds]UDPBinding
	udpLock     Spinlock // lock-ordering rank 7
)

// udpParse returns the header + data slice. ok=false if the buffer
// is too short.
func udpParse(packet []byte) (UDPHeader, []byte, bool) {
	if len(packet) < udpHeaderSize {
		return UDPHeader{}, nil, false
	}
	var h UDPHeader
	h.SrcPort = uint16(packet[0])<<8 | uint16(packet[1])
	h.DstPort = uint16(packet[2])<<8 | uint16(packet[3])
	h.Length = uint16(packet[4])<<8 | uint16(packet[5])
	h.Chksum = uint16(packet[6])<<8 | uint16(packet[7])
	if int(h.Length) > len(packet) || h.Length < udpHeaderSize {
		return h, nil, false
	}
	return h, packet[udpHeaderSize:h.Length], true
}

// udpChecksum computes the UDP checksum over the pseudo-header +
// `udpPacket` (which holds the 8-byte UDP header with Chksum=0 plus the
// payload). Returns 0xFFFF in place of 0 per RFC 768.
func udpChecksum(srcIP, dstIP uint32, udpPacket []byte) uint16 {
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
	pseudo[9] = ipProtoUDP
	l := uint16(len(udpPacket))
	pseudo[10] = byte(l >> 8)
	pseudo[11] = byte(l)

	sum := uint32(0)
	for i := 0; i+1 < len(pseudo); i += 2 {
		sum += uint32(pseudo[i])<<8 | uint32(pseudo[i+1])
	}
	i := 0
	for ; i+1 < len(udpPacket); i += 2 {
		sum += uint32(udpPacket[i])<<8 | uint32(udpPacket[i+1])
	}
	if i < len(udpPacket) {
		sum += uint32(udpPacket[i]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	result := ^uint16(sum)
	if result == 0 {
		return 0xFFFF
	}
	return result
}

// udpChecksumVerify returns true when the RX packet's checksum is
// either 0 (disabled) or matches the computed value.
func udpChecksumVerify(srcIP, dstIP uint32, packet []byte) bool {
	if len(packet) < udpHeaderSize {
		return false
	}
	wire := uint16(packet[6])<<8 | uint16(packet[7])
	if wire == 0 {
		return true // checksum not used
	}
	// Recompute with the Chksum field zeroed.
	scratch := make([]byte, len(packet))
	copy(scratch, packet)
	scratch[6] = 0
	scratch[7] = 0
	computed := udpChecksum(srcIP, dstIP, scratch)
	// Both 0xFFFF and the original `wire` value should match;
	// also accept the rare case where wire==0xFFFF and the raw sum
	// was 0 (RFC 768 translation).
	return computed == wire
}

// udpBind reserves `port` and returns a receive channel (cap=16) on
// which incoming datagrams to that port arrive. Returns nil when the
// port is taken or the table is full.
func udpBind(port uint16) chan UDPDatagram {
	flags := udpLock.Acquire()
	defer udpLock.Release(flags)
	for i := 0; i < udpMaxBinds; i++ {
		if udpBindings[i].Active && udpBindings[i].Port == port {
			return nil
		}
	}
	for i := 0; i < udpMaxBinds; i++ {
		if !udpBindings[i].Active {
			udpBindings[i] = UDPBinding{
				Port:   port,
				Ch:     make(chan UDPDatagram, 16),
				Active: true,
			}
			return udpBindings[i].Ch
		}
	}
	return nil
}

// udpUnbind releases `port`'s slot. No-op if the port is not bound.
func udpUnbind(port uint16) {
	flags := udpLock.Acquire()
	defer udpLock.Release(flags)
	for i := 0; i < udpMaxBinds; i++ {
		if udpBindings[i].Active && udpBindings[i].Port == port {
			udpBindings[i].Active = false
			udpBindings[i].Ch = nil
		}
	}
}

// udpLookupChannel returns the listener channel for `port`, or nil.
func udpLookupChannel(port uint16) chan UDPDatagram {
	flags := udpLock.Acquire()
	defer udpLock.Release(flags)
	for i := 0; i < udpMaxBinds; i++ {
		if udpBindings[i].Active && udpBindings[i].Port == port {
			return udpBindings[i].Ch
		}
	}
	return nil
}

// udpHandle is called by ipv4Handle when Protocol == 17.
func udpHandle(hdr IPv4Header, inner []byte) {
	uh, data, ok := udpParse(inner)
	if !ok {
		return
	}
	if !udpChecksumVerify(hdr.SrcIP, hdr.DstIP, inner[:uh.Length]) {
		statsInc(&netStats.ChecksumErr)
		return
	}
	ch := udpLookupChannel(uh.DstPort)
	if ch == nil {
		statsInc(&netStats.UdpPortUnreach)
		return
	}
	// Copy the payload — `data` points into the shared RX buffer that
	// netRxLoop will reuse on the next descriptor drain.
	cp := make([]byte, len(data))
	copy(cp, data)

	// Non-blocking delivery: drop if the listener is back-pressured.
	select {
	case ch <- UDPDatagram{
		SrcIP: hdr.SrcIP, SrcPort: uh.SrcPort, DstPort: uh.DstPort, Data: cp,
	}:
		statsInc(&netStats.UdpRecv)
	default:
		statsInc(&netStats.RxDropped)
	}
}

// udpSend transmits a UDP datagram. Returns false on oversize payload,
// ARP failure, or TX ring full.
func udpSend(dstIP uint32, dstPort, srcPort uint16, data []byte) bool {
	if len(data)+udpHeaderSize > ipv4MaxPayload {
		return false
	}
	packet := make([]byte, udpHeaderSize+len(data))
	length := uint16(len(packet))
	packet[0] = byte(srcPort >> 8)
	packet[1] = byte(srcPort)
	packet[2] = byte(dstPort >> 8)
	packet[3] = byte(dstPort)
	packet[4] = byte(length >> 8)
	packet[5] = byte(length)
	packet[6] = 0
	packet[7] = 0 // checksum placeholder
	copy(packet[udpHeaderSize:], data)

	csum := udpChecksum(ourIP, dstIP, packet)
	packet[6] = byte(csum >> 8)
	packet[7] = byte(csum)

	ok := ipv4Send(ipProtoUDP, dstIP, packet)
	if ok {
		statsInc(&netStats.UdpSend)
	}
	return ok
}

// udpEchoServer binds port 7 and reflects every received datagram back
// to the sender. Started as a goroutine from netInit.
func udpEchoServer() {
	ch := udpBind(7)
	if ch == nil {
		serialPrintln("UDP echo: port 7 bind failed")
		return
	}
	serialPrintln("UDP echo: listening on port 7")
	for {
		dg := <-ch
		udpSend(dg.SrcIP, dg.SrcPort, dg.DstPort, dg.Data)
		runtime.Gosched()
	}
}
