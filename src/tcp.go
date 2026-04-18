package main

// TCP transport — scaffolded across Phase TCP-1..TCP-5 per
// impldoc/net_tcp_work_plan.md §2. This file will grow to carry
// the TCB table, state machine, and listener bookkeeping in
// subsequent commits inside Phase TCP-1.

// tcpHandle is the RX dispatcher for TCP segments, called from
// ipv4Handle's protocol switch (src/ipv4.go). Until the TCB
// machinery comes online later in Phase TCP-1, incoming
// segments are silently dropped — a peer that probes TCP will
// see the kernel as "nothing listening" (no RST yet).
func tcpHandle(hdr IPv4Header, inner []byte) {
	_ = hdr
	_ = inner
}
