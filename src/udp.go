// src/udp.go -- User Datagram Protocol (RFC 768) over IPv4.
//
// Exposes a kernel-internal bind table (8 entries) so in-kernel
// services can receive UDP traffic through a bounded MPSC queue
// (M4.2.b: udpDgramQueue, replacing chan UDPDatagram). The Phase 5
// socket syscall API layers on top of this; the built-in
// `udpEchoServer` (kthread per M4.2.b) is one consumer, socketFd
// is the other.
//
// Checksums are computed over the pseudo-header + UDP header + data.
// RFC 768 permits sending a zero checksum on IPv4 (disables validation
// on RX); we always compute on TX and translate a zero result to
// 0xFFFF as required.

package main

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

// UDPBinding associates a port with a receive queue (M4.2.b:
// was chan UDPDatagram, now udpDgramQueue MPSC).
type UDPBinding struct {
	Port   uint16
	Q      *udpDgramQueue
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

// udpBind reserves `port` and returns a fresh receive queue
// (cap=udpDgramQueueCap=16) on which incoming datagrams arrive.
// Returns nil when the port is taken or the table is full.
func udpBind(port uint16) *udpDgramQueue {
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
				Q:      newUdpDgramQueue(),
				Active: true,
			}
			return udpBindings[i].Q
		}
	}
	return nil
}

// udpBindWithQueue is the Phase-5 variant used by socketFd: the
// caller supplies its own receive queue (so the same queue can
// move with the socket across bind/close). Returns false on port
// collision or table exhaustion. M4.2.b: was udpBindWithChannel
// taking a chan UDPDatagram.
func udpBindWithQueue(port uint16, q *udpDgramQueue) bool {
	if q == nil {
		return false
	}
	flags := udpLock.Acquire()
	defer udpLock.Release(flags)
	for i := 0; i < udpMaxBinds; i++ {
		if udpBindings[i].Active && udpBindings[i].Port == port {
			return false
		}
	}
	for i := 0; i < udpMaxBinds; i++ {
		if !udpBindings[i].Active {
			udpBindings[i] = UDPBinding{
				Port:   port,
				Q:      q,
				Active: true,
			}
			return true
		}
	}
	return false
}

// udpUnbind releases `port`'s slot. No-op if the port is not bound.
func udpUnbind(port uint16) {
	flags := udpLock.Acquire()
	defer udpLock.Release(flags)
	for i := 0; i < udpMaxBinds; i++ {
		if udpBindings[i].Active && udpBindings[i].Port == port {
			udpBindings[i].Active = false
			udpBindings[i].Q = nil
		}
	}
}

// udpLookupQueue returns the listener queue for `port`, or nil.
func udpLookupQueue(port uint16) *udpDgramQueue {
	flags := udpLock.Acquire()
	defer udpLock.Release(flags)
	for i := 0; i < udpMaxBinds; i++ {
		if udpBindings[i].Active && udpBindings[i].Port == port {
			return udpBindings[i].Q
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
	q := udpLookupQueue(uh.DstPort)
	if q == nil {
		statsInc(&netStats.UdpPortUnreach)
		return
	}
	// Copy the payload — `data` points into the shared RX buffer that
	// netRxLoop will reuse on the next descriptor drain.
	cp := make([]byte, len(data))
	copy(cp, data)

	// Non-blocking delivery: drop if the listener is back-pressured.
	// M4.2.b: queue.TryPush replaces the old `select { ch <- dg:
	// default: drop }` semantics one-for-one.
	if q.TryPush(UDPDatagram{
		SrcIP: hdr.SrcIP, SrcPort: uh.SrcPort, DstPort: uh.DstPort, Data: cp,
	}) {
		statsInc(&netStats.UdpRecv)
	} else {
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

// udpSendRaw transmits a UDP packet with caller-chosen source / dest
// IPs, skipping ARP and hard-wiring the destination MAC to the
// broadcast address. Intended for DHCP, where the client must send
// from 0.0.0.0:68 to 255.255.255.255:67 before it has either an IP of
// its own or an ARP binding for the server.
//
// Returns false on payload-too-large or TX-ring-full.
func udpSendRaw(srcIP, dstIP uint32, srcPort, dstPort uint16, data []byte) bool {
	if !e1000Found {
		return false
	}
	udpLen := udpHeaderSize + len(data)
	if udpLen+ipv4HeaderMinSize > ipv4MTU {
		return false
	}
	packet := make([]byte, udpLen)
	packet[0] = byte(srcPort >> 8)
	packet[1] = byte(srcPort)
	packet[2] = byte(dstPort >> 8)
	packet[3] = byte(dstPort)
	packet[4] = byte(udpLen >> 8)
	packet[5] = byte(udpLen)
	packet[6] = 0
	packet[7] = 0
	copy(packet[udpHeaderSize:], data)
	csum := udpChecksum(srcIP, dstIP, packet)
	packet[6] = byte(csum >> 8)
	packet[7] = byte(csum)

	totalLen := ipv4HeaderMinSize + udpLen
	frame := make([]byte, ethernetHeaderSize+totalLen)
	broadcastMAC := broadcastMACAddr()
	copy(frame[0:6], broadcastMAC[:])
	copy(frame[6:12], e1000MAC[:])
	frame[12] = byte(etherTypeIPv4 >> 8)
	frame[13] = byte(etherTypeIPv4 & 0xFF)

	ipv4BuildHeader(frame[ethernetHeaderSize:], ipProtoUDP, srcIP, dstIP, udpLen)
	copy(frame[ethernetHeaderSize+ipv4HeaderMinSize:], packet)

	ok := e1000Transmit(frame)
	if ok {
		statsInc(&netStats.UdpSend)
	}
	return ok
}

// udpEchoServer binds port 7 and reflects every received datagram back
// to the sender. Started as a kthread from netInit (M4.2.b: was a
// goroutine pre-Route-C).
func udpEchoServer() {
	q := udpBind(7)
	if q == nil {
		serialPrintln("UDP echo: port 7 bind failed")
		return
	}
	serialPrintln("UDP echo: listening on port 7")
	for {
		dg := q.Pop() // blocking; parks the kthread when empty
		udpSend(dg.SrcIP, dg.SrcPort, dg.DstPort, dg.Data)
	}
}
