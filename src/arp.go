// src/arp.go -- ARP (Address Resolution Protocol) for IPv4 over Ethernet.
//
// Fixed 16-entry cache with LRU replacement, protected by arpLock
// (ordering rank 6). arpResolve blocks up to 2 seconds polling the
// cache after sending a request; arpHandle learns from every valid
// ARP packet and sends a reply when the request targets our IP.
//
// All multi-byte wire fields are assembled byte-by-byte (big-endian)
// so there is no dependence on Go struct layout.

package main

import "runtime"

const (
	arpHeaderSize  = 28
	arpCacheSize   = 16
	arpResolveTicks = uint64(200) // 2 seconds at 100 Hz PIT
)

// ARP operation codes (big-endian on the wire; stored host-order in
// parsed structs).
const (
	arpOpRequest = uint16(1)
	arpOpReply   = uint16(2)
)

// Hardware type = Ethernet (1). Protocol type = IPv4 (0x0800).
const (
	arpHwEthernet  = uint16(0x0001)
	arpProtoIPv4   = uint16(0x0800)
)

// ARPPacket is a host-order view of an ARP-over-Ethernet IPv4 packet.
type ARPPacket struct {
	HWType, ProtoType uint16
	HWLen, ProtoLen   uint8
	Op                uint16
	SHA               [6]byte
	SPA               uint32
	THA               [6]byte
	TPA               uint32
}

// ARPEntry is a single slot in the ARP cache.
type ARPEntry struct {
	IP   uint32
	MAC  [6]byte
	Age  uint64 // pitTicks at last update
	Used bool
}

var (
	arpCache [arpCacheSize]ARPEntry
	arpLock  Spinlock // lock-ordering rank 6
)

// arpParse decodes an ARP packet payload (the portion after the
// Ethernet header). Returns ok=false for any structural mismatch.
func arpParse(payload []byte) (ARPPacket, bool) {
	if len(payload) < arpHeaderSize {
		return ARPPacket{}, false
	}
	var p ARPPacket
	p.HWType = uint16(payload[0])<<8 | uint16(payload[1])
	p.ProtoType = uint16(payload[2])<<8 | uint16(payload[3])
	p.HWLen = payload[4]
	p.ProtoLen = payload[5]
	p.Op = uint16(payload[6])<<8 | uint16(payload[7])
	if p.HWType != arpHwEthernet || p.ProtoType != arpProtoIPv4 ||
		p.HWLen != 6 || p.ProtoLen != 4 {
		return p, false
	}
	copy(p.SHA[:], payload[8:14])
	p.SPA = uint32(payload[14])<<24 | uint32(payload[15])<<16 |
		uint32(payload[16])<<8 | uint32(payload[17])
	copy(p.THA[:], payload[18:24])
	p.TPA = uint32(payload[24])<<24 | uint32(payload[25])<<16 |
		uint32(payload[26])<<8 | uint32(payload[27])
	return p, true
}

// arpBuild serializes an ARP packet (without the Ethernet header).
// Returns a freshly-allocated 28-byte slice.
func arpBuild(op uint16, sha [6]byte, spa uint32, tha [6]byte, tpa uint32) []byte {
	out := make([]byte, arpHeaderSize)
	out[0] = byte(arpHwEthernet >> 8)
	out[1] = byte(arpHwEthernet & 0xFF)
	out[2] = byte(arpProtoIPv4 >> 8)
	out[3] = byte(arpProtoIPv4 & 0xFF)
	out[4] = 6
	out[5] = 4
	out[6] = byte(op >> 8)
	out[7] = byte(op)
	copy(out[8:14], sha[:])
	out[14] = byte(spa >> 24)
	out[15] = byte(spa >> 16)
	out[16] = byte(spa >> 8)
	out[17] = byte(spa)
	copy(out[18:24], tha[:])
	out[24] = byte(tpa >> 24)
	out[25] = byte(tpa >> 16)
	out[26] = byte(tpa >> 8)
	out[27] = byte(tpa)
	return out
}

// arpLookup returns the MAC associated with `ip` if present, else false.
func arpLookup(ip uint32) ([6]byte, bool) {
	flags := arpLock.Acquire()
	for i := 0; i < arpCacheSize; i++ {
		if arpCache[i].Used && arpCache[i].IP == ip {
			mac := arpCache[i].MAC
			arpLock.Release(flags)
			statsInc(&netStats.ArpHits)
			return mac, true
		}
	}
	arpLock.Release(flags)
	statsInc(&netStats.ArpMisses)
	return zeroMAC, false
}

// arpLearn inserts or refreshes an (ip, mac) entry. When the cache is
// full, replaces the oldest entry (smallest Age).
func arpLearn(ip uint32, mac [6]byte) {
	flags := arpLock.Acquire()
	defer arpLock.Release(flags)

	// Refresh existing entry.
	for i := 0; i < arpCacheSize; i++ {
		if arpCache[i].Used && arpCache[i].IP == ip {
			arpCache[i].MAC = mac
			arpCache[i].Age = pitTicks
			return
		}
	}

	// Find a free slot.
	for i := 0; i < arpCacheSize; i++ {
		if !arpCache[i].Used {
			arpCache[i] = ARPEntry{IP: ip, MAC: mac, Age: pitTicks, Used: true}
			return
		}
	}

	// Evict the oldest.
	oldest := 0
	for i := 1; i < arpCacheSize; i++ {
		if arpCache[i].Age < arpCache[oldest].Age {
			oldest = i
		}
	}
	arpCache[oldest] = ARPEntry{IP: ip, MAC: mac, Age: pitTicks, Used: true}
}

// arpSendRequest broadcasts a "who-has targetIP tell ourIP" query.
func arpSendRequest(targetIP uint32) {
	if !e1000Found {
		return
	}
	payload := arpBuild(arpOpRequest, e1000MAC, ourIP, zeroMAC, targetIP)
	frame := ethernetBuild(broadcastMAC, e1000MAC, etherTypeARP, payload)
	if e1000Transmit(frame) {
		statsInc(&netStats.ArpRequestsSent)
	}
}

// arpSendReply unicasts an ARP reply to `dstMAC`, announcing our MAC
// for our IP.
func arpSendReply(dstMAC [6]byte, dstIP uint32) {
	if !e1000Found {
		return
	}
	payload := arpBuild(arpOpReply, e1000MAC, ourIP, dstMAC, dstIP)
	frame := ethernetBuild(dstMAC, e1000MAC, etherTypeARP, payload)
	if e1000Transmit(frame) {
		statsInc(&netStats.ArpRepliesSent)
	}
}

// arpSendGratuitous announces our IP/MAC to the local segment by
// broadcasting an ARP reply that maps our IP to our MAC. Used at boot
// so neighbours learn our identity without waiting for a request.
func arpSendGratuitous() {
	if !e1000Found || ourIP == 0 {
		return
	}
	payload := arpBuild(arpOpReply, e1000MAC, ourIP, broadcastMAC, ourIP)
	frame := ethernetBuild(broadcastMAC, e1000MAC, etherTypeARP, payload)
	if e1000Transmit(frame) {
		statsInc(&netStats.ArpRepliesSent)
	}
	serialPrintln("ARP: sent gratuitous announcement for " + ipToString(ourIP))
}

// arpHandle processes an ARP packet received on the wire. Learns the
// sender's (SPA, SHA) binding unconditionally, and replies when the
// request targets our IP.
func arpHandle(srcMAC [6]byte, payload []byte) {
	_ = srcMAC // the payload's SHA is authoritative; srcMAC is redundant but kept for symmetry.
	p, ok := arpParse(payload)
	if !ok {
		return
	}
	// Always update our cache with the sender's binding.
	if p.SPA != 0 {
		arpLearn(p.SPA, p.SHA)
	}
	// Reply to requests targeting us.
	if p.Op == arpOpRequest && p.TPA == ourIP && ourIP != 0 {
		arpSendReply(p.SHA, p.SPA)
	}
}

// arpResolve looks up `ip` in the cache; on miss, broadcasts a request
// and then polls the cache with cooperative yields until either the
// reply lands or the 2-second timeout elapses. Returns the MAC (or
// zeroMAC) and a success flag.
func arpResolve(ip uint32) ([6]byte, bool) {
	if mac, ok := arpLookup(ip); ok {
		return mac, true
	}
	arpSendRequest(ip)
	timeout := afterTicks(arpResolveTicks)
	for {
		select {
		case <-timeout:
			if mac, ok := arpLookup(ip); ok {
				return mac, true
			}
			return zeroMAC, false
		default:
		}
		if mac, ok := arpLookup(ip); ok {
			return mac, true
		}
		runtime.Gosched()
	}
}
