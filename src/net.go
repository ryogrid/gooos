// src/net.go -- Top-level networking orchestrator.
//
// Holds static IP configuration (QEMU slirp defaults), drives the RX
// dispatch goroutine that pulls completed frames off the e1000 ring
// and hands them to the Ethernet / ARP / IPv4 handlers, and exposes
// the `netInit` entry point invoked from main.go after e1000Init.
//
// Phase 2 uses a simple poll-plus-yield RX loop; Phase 4 rewires the
// loop to wait on `rxSignalCh` from the e1000 ISR.

package main

import "runtime"

// Static IP configuration — matches the QEMU user-mode slirp defaults
// (guest = 10.0.2.15, gateway / DNS / TFTP server = 10.0.2.2).
var (
	ourIP      uint32
	ourNetmask uint32
	ourGateway uint32
)

// netInit configures the static address block, sends a gratuitous ARP
// so the gateway learns our binding, and spawns the RX dispatch
// goroutine.
//
// Must be called after pciInit + e1000Init. No-ops silently if no NIC
// was found (callers gate on e1000Found before dialling in any net
// service).
func netInit() {
	if !e1000Found {
		return
	}

	ourIP = parseIPv4("10.0.2.15")
	ourNetmask = parseIPv4("255.255.255.0")
	ourGateway = parseIPv4("10.0.2.2")

	serialPrintln("NET: initialized IP=" + ipToString(ourIP) +
		" gw=" + ipToString(ourGateway))

	arpSendGratuitous()

	go netRxLoop()
	serialPrintln("NET: RX dispatch goroutine started")
}

// netRxLoop pulls one completed RX descriptor at a time and runs the
// Ethernet dispatcher on it. Yields the CPU when the ring is empty so
// the scheduler can run other goroutines; Phase 4 replaces the poll
// with a channel wait on rxSignalCh.
func netRxLoop() {
	for {
		frame := e1000TryReceive()
		if frame == nil {
			runtime.Gosched()
			continue
		}
		ethernetDispatch(frame)
	}
}

// ethernetDispatch runs one frame through frame parsing, the "for us"
// filter, and the EtherType switch. Called by netRxLoop for every RX
// frame; in Phase 3 the IPv4 case wires in.
func ethernetDispatch(frame []byte) {
	if len(frame) < ethernetHeaderSize {
		return
	}
	hdr, payload, ok := ethernetParse(frame)
	if !ok {
		return
	}
	if !isForUs(hdr.Dst) {
		return
	}
	switch hdr.EtherType {
	case etherTypeARP:
		arpHandle(hdr.Src, payload)
	case etherTypeIPv4:
		ipv4Handle(payload)
	default:
		// Unknown EtherType — drop silently.
	}
}

// nextHopIP returns the IP we actually ARP for when sending to `dst`:
// `dst` itself when it is on our subnet, else the configured gateway.
func nextHopIP(dst uint32) uint32 {
	if ourNetmask != 0 && (dst&ourNetmask) == (ourIP&ourNetmask) {
		return dst
	}
	return ourGateway
}

// netDiag prints a short identity block to serial. Phase 4 fills this
// out with the ARP cache and statistics counters.
func netDiag() {
	serialPrintln("=== Network Diagnostics ===")
	serialPrintln("MAC: " + macToString(e1000MAC))
	serialPrintln("IP:  " + ipToString(ourIP))
	serialPrintln("GW:  " + ipToString(ourGateway))
}
