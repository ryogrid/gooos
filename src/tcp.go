package main

// TCP transport — scaffolded across Phase TCP-1..TCP-5 per
// impldoc/net_tcp_work_plan.md §2. This file carries the TCB
// table, state enum, and allocation primitives; the state
// machine, listener table, ring-buffer methods, and send path
// land in subsequent commits in this phase.

// tcbState is one of tcpStateClosed..tcpStateTimeWait per RFC 793.
type tcbState uint8

const (
	tcpStateClosed tcbState = iota
	tcpStateListen
	tcpStateSynSent
	tcpStateSynReceived
	tcpStateEstablished
	tcpStateFinWait1
	tcpStateFinWait2
	tcpStateCloseWait
	tcpStateClosing
	tcpStateLastAck
	tcpStateTimeWait
)

// TCB — Transmission Control Block. Protected by tcbTableLock
// (rank 9). One TCB per active connection; fixed-size table
// keeps memory bounded and avoids allocation on the RX path.
// Field set grows across Phase TCP-1..TCP-4 commits; this
// initial version carries the minimum needed for allocation
// and 4-tuple lookup.
type TCB struct {
	// 4-tuple identity (host byte order).
	localIP    uint32
	localPort  uint16
	remoteIP   uint32
	remotePort uint16

	// State.
	state tcbState

	// Bookkeeping.
	userOwner int  // owning pid; -1 = kernel-internal
	active    bool // false = slot is free
}

// tcbMax caps concurrent TCP connections. See TD2 in
// impldoc/net_tcp_overview.md §5.
const tcbMax = 16

var (
	tcbTable     [tcbMax]TCB
	tcbTableLock Spinlock // lock ordering rank 9
)

// tcbAlloc claims a free TCB slot and initialises it for the
// given 4-tuple, state tcpStateClosed, userOwner=-1. Returns
// nil if the table is full. Caller must NOT hold tcbTableLock;
// this function acquires it.
func tcbAlloc(localIP uint32, localPort uint16,
	remoteIP uint32, remotePort uint16) *TCB {
	flags := tcbTableLock.Acquire()
	defer tcbTableLock.Release(flags)
	for i := 0; i < tcbMax; i++ {
		t := &tcbTable[i]
		if !t.active {
			t.localIP = localIP
			t.localPort = localPort
			t.remoteIP = remoteIP
			t.remotePort = remotePort
			t.state = tcpStateClosed
			t.userOwner = -1
			t.active = true
			return t
		}
	}
	return nil
}

// tcbFree releases a TCB slot. Caller must NOT hold
// tcbTableLock; this function acquires it. Later phases extend
// this to cancel timers, drain buffers, and close wakeup
// channels before marking active=false.
func tcbFree(t *TCB) {
	if t == nil {
		return
	}
	flags := tcbTableLock.Acquire()
	defer tcbTableLock.Release(flags)
	t.active = false
	t.state = tcpStateClosed
	t.localIP = 0
	t.localPort = 0
	t.remoteIP = 0
	t.remotePort = 0
	t.userOwner = -1
}

// tcbLookup returns the active TCB matching the full 4-tuple,
// or nil if no match. Caller must NOT hold tcbTableLock; this
// function acquires it.
func tcbLookup(localIP uint32, localPort uint16,
	remoteIP uint32, remotePort uint16) *TCB {
	flags := tcbTableLock.Acquire()
	defer tcbTableLock.Release(flags)
	for i := 0; i < tcbMax; i++ {
		t := &tcbTable[i]
		if !t.active {
			continue
		}
		if t.localIP == localIP && t.localPort == localPort &&
			t.remoteIP == remoteIP && t.remotePort == remotePort {
			return t
		}
	}
	return nil
}

// tcpHandle is the RX dispatcher for TCP segments, called from
// ipv4Handle's protocol switch (src/ipv4.go). Full state-
// machine dispatch lands in a subsequent Phase TCP-1 commit;
// this interim version silently drops segments (no RST yet).
func tcpHandle(hdr IPv4Header, inner []byte) {
	_ = hdr
	_ = inner
}
