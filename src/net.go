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
//
// ourDNS is updated by the userspace DHCP client via sys_net_config;
// at boot it is zero until the client runs. No in-kernel DNS resolver
// uses it yet — it is exposed for userspace programs.
var (
	ourIP      uint32
	ourNetmask uint32
	ourGateway uint32
	ourDNS     uint32
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

	go udpEchoServer()

	tcpInit()
}

// netRxLoop drives the receive side. Simplest possible poller:
// drainRxRing, yield, repeat. No channel, no flag, no sti/hlt.
// The previous channel-based design (rxSignalCh + ISR send) hit
// an unsolvable race where ISR-context channel sends couldn't
// wake a parked receiver under gooos's cooperative scheduler.
// Polling is slightly more CPU-hungry but trivially correct.
func netRxLoop() {
	for {
		drainRxRing()
		statsInc(&netStats.NetRxLoopWakes) // counts iterations
		runtime.Gosched()
	}
}

// drainRxRing consumes every DD-marked RX descriptor currently
// available and runs the Ethernet dispatcher on each frame.
func drainRxRing() {
	for {
		frame := e1000TryReceive()
		if frame == nil {
			return
		}
		statsInc(&netStats.NetRxFrames)
		statsInc(&netStats.RxPackets)
		statsAdd(&netStats.RxBytes, uint64(len(frame)))
		ethernetDispatch(frame)
	}
}

// ethernetDispatch runs one frame through frame parsing, the "for us"
// filter, and the EtherType switch. Called by netRxLoop for every RX
// frame; in Phase 3 the IPv4 case wires in.
func ethernetDispatch(frame []byte) {
	// Runt / oversize frames are dropped up front. The NIC already
	// rejects most malformed traffic, but the design doc specifies
	// explicit validation so RxDropped reflects the host policy.
	if len(frame) < ethernetMinRxFrame || len(frame) > ethernetMaxRxFrame {
		statsInc(&netStats.RxDropped)
		return
	}
	hdr, payload, ok := ethernetParse(frame)
	if !ok {
		statsInc(&netStats.RxDropped)
		return
	}
	if !isForUs(hdr.Dst) {
		statsInc(&netStats.RxDropped)
		return
	}
	switch hdr.EtherType {
	case etherTypeARP:
		arpHandle(hdr.Src, payload)
	case etherTypeIPv4:
		ipv4Handle(payload)
	default:
		statsInc(&netStats.RxUnknownEtherType)
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

// netDiag prints the full network stack state to serial: link /
// MAC / IP / gateway, the live ARP cache, and every counter in
// netStats. Invoked once automatically ~5 s after netInit by a
// goroutine spawned from main.go.
func netDiag() {
	serialPrintln("=== Network Diagnostics ===")

	if e1000Found {
		status := e1000Read(e1000STATUS)
		if status&e1000StatusLU != 0 {
			serialPrintln("Link: UP")
		} else {
			serialPrintln("Link: DOWN")
		}
	} else {
		serialPrintln("Link: (no NIC)")
	}
	serialPrintln("MAC: " + macToString(e1000MAC))
	serialPrintln("IP:  " + ipToString(ourIP))
	serialPrintln("GW:  " + ipToString(ourGateway))
	serialPrintln("DNS: " + ipToString(ourDNS))

	serialPrintln("ARP cache:")
	flags := arpLock.Acquire()
	any := false
	for i := 0; i < arpCacheSize; i++ {
		if arpCache[i].Used {
			line := "  " + ipToString(arpCache[i].IP) + " -> " +
				macToString(arpCache[i].MAC)
			arpLock.Release(flags)
			serialPrintln(line)
			flags = arpLock.Acquire()
			any = true
		}
	}
	arpLock.Release(flags)
	if !any {
		serialPrintln("  (empty)")
	}

	s := netStatsSnapshot()
	serialPrintln("TX: " + utoa(s.TxPackets) + " pkts, " + utoa(s.TxBytes) + " bytes")
	serialPrintln("RX: " + utoa(s.RxPackets) + " pkts, " + utoa(s.RxBytes) + " bytes")
	serialPrintln("RxDropped: " + utoa(s.RxDropped) +
		"  RxUnknownEtherType: " + utoa(s.RxUnknownEtherType))
	serialPrintln("ARP: hits=" + utoa(s.ArpHits) +
		" misses=" + utoa(s.ArpMisses) +
		" req=" + utoa(s.ArpRequestsSent) +
		" rep=" + utoa(s.ArpRepliesSent))
	serialPrintln("IPv4: ChecksumErr=" + utoa(s.ChecksumErr) +
		" FragmentsDropped=" + utoa(s.FragmentsDropped))
	serialPrintln("ICMP echo: " + utoa(s.IcmpEcho))
	serialPrintln("UDP: recv=" + utoa(s.UdpRecv) +
		" send=" + utoa(s.UdpSend) +
		" portUnreach=" + utoa(s.UdpPortUnreach))
	serialPrintln("Buf alloc fails: " + utoa(s.BufAllocFail))
	serialPrintln("RX pipeline: e1000IRQs=" + utoa(e1000IRQCount) +
		" idleParks=" + utoa(s.NetRxLoopWakes) +
		" netRxFrames=" + utoa(s.NetRxFrames) +
		" pitTicks=" + utoa(pitTicks))
	serialPrintln("Sched: afterTicksCalls=" + utoa(afterTicksCalls))
	serialPrintln("KBD: irqs=" + utoa(kbdIRQCount) +
		" pumpDrained=" + utoa(kbdPumpDrainCount) +
		" wakeupIPIs=" + utoa(wakeupIPICount))
	tcpDiag()
	serialPrintln("=== end ===")
}
