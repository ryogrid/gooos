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

// Per-TCB ring-buffer capacities (bytes). Equal size for TX and
// RX per TD6 in impldoc/net_tcp_overview.md §5. Must be a power
// of two so tcpBufMask below is valid.
const (
	tcpTxBufSize = 8192
	tcpRxBufSize = 8192
	tcpBufMask   = uint32(tcpRxBufSize - 1) // 0x1FFF
)

// tcpRingBuf is a byte-granular FIFO used for per-TCB send and
// receive buffering. head / tail are monotonically increasing
// logical counters; the physical index is (counter & tcpBufMask).
// count tracks buffered bytes so empty (count==0) and full
// (count==cap) are distinguishable without a reserved slot.
// See impldoc/net_tcp_buffers.md §3 for the design.
type tcpRingBuf struct {
	data  [tcpRxBufSize]byte
	head  uint32 // logical read index (bytes consumed)
	tail  uint32 // logical write index (bytes appended)
	count uint32 // bytes currently buffered
}

// TCB — Transmission Control Block. Protected by tcbTableLock
// (rank 9). One TCB per active connection; fixed-size table
// keeps memory bounded and avoids allocation on the RX path.
// Field set grows across Phase TCP-1..TCP-4 commits.
type TCB struct {
	// 4-tuple identity (host byte order).
	localIP    uint32
	localPort  uint16
	remoteIP   uint32
	remotePort uint16

	// State.
	state tcbState

	// Send sequence space (RFC 793 §3.2).
	sndUna uint32 // oldest unacknowledged sequence number
	sndNxt uint32 // next sequence number to send
	sndWnd uint32 // peer-advertised receive window (bytes)
	sndWl1 uint32 // seq of last segment used to update sndWnd
	sndWl2 uint32 // ack of last segment used to update sndWnd
	iss    uint32 // our initial send sequence number

	// Receive sequence space (RFC 793 §3.2).
	rcvNxt uint32 // next expected sequence number
	rcvWnd uint32 // our advertised receive window (bytes)
	irs    uint32 // peer's initial send sequence number

	// MSS negotiation. mssEff = min(mssLocal, mssPeer); used on TX.
	mssLocal uint16
	mssPeer  uint16
	mssEff   uint16

	// Send + receive ring buffers (see net_tcp_buffers.md §3).
	txBuf tcpRingBuf
	rxBuf tcpRingBuf

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

// --- ring-buffer primitives ---

// rbFree returns the number of bytes that can be written
// before the buffer becomes full.
func (r *tcpRingBuf) rbFree() int {
	return tcpRxBufSize - int(r.count)
}

// rbLen returns the number of bytes currently buffered.
func (r *tcpRingBuf) rbLen() int {
	return int(r.count)
}

// rbCap returns the total ring capacity (always tcpRxBufSize).
func (r *tcpRingBuf) rbCap() int {
	return tcpRxBufSize
}

// rbReset empties the ring in place.
func (r *tcpRingBuf) rbReset() {
	r.head = 0
	r.tail = 0
	r.count = 0
}

// rbWrite copies up to len(src) bytes into the ring at the
// tail. Returns the actual number of bytes written; fewer than
// len(src) means the ring filled mid-copy.
func (r *tcpRingBuf) rbWrite(src []byte) int {
	n := len(src)
	if n > r.rbFree() {
		n = r.rbFree()
	}
	for i := 0; i < n; i++ {
		r.data[(r.tail+uint32(i))&tcpBufMask] = src[i]
	}
	r.tail += uint32(n)
	r.count += uint32(n)
	return n
}

// rbRead copies up to len(dst) bytes out of the ring from the
// head (destructive). Returns the actual number of bytes read;
// fewer than len(dst) means the ring emptied mid-copy.
func (r *tcpRingBuf) rbRead(dst []byte) int {
	n := len(dst)
	if n > r.rbLen() {
		n = r.rbLen()
	}
	for i := 0; i < n; i++ {
		dst[i] = r.data[(r.head+uint32(i))&tcpBufMask]
	}
	r.head += uint32(n)
	r.count -= uint32(n)
	return n
}

// rbPeek copies n bytes starting at offset `off` (relative to
// head) into dst WITHOUT advancing head. Used by the
// retransmission queue to rebuild an in-flight segment from its
// descriptor. Caller must ensure off+n <= rbLen() and
// len(dst) >= n; otherwise rbPeek stops early.
func (r *tcpRingBuf) rbPeek(off, n uint32, dst []byte) {
	if uint32(len(dst)) < n {
		n = uint32(len(dst))
	}
	avail := uint32(r.rbLen())
	if off >= avail {
		return
	}
	if off+n > avail {
		n = avail - off
	}
	for i := uint32(0); i < n; i++ {
		dst[i] = r.data[(r.head+off+i)&tcpBufMask]
	}
}

// --- ISN generator and send path ---

// isnNext returns a fresh initial-sequence-number candidate.
// RFC 793 §3.3 recommends a ~4-µs-period clock; gooos has only
// a 10 ms PIT, so we scale by 250 000 to approximate a 25 MHz
// counter. Predictable ISN is a known hobby-OS threat (risk
// TR10 in impldoc/net_tcp_overview.md §9); accepted.
func isnNext() uint32 {
	return uint32(pitTicks * 250000)
}

// tcpScratchSize is the stack-local buffer reserved for the
// outbound-segment build in tcpSendSegment. It accommodates a
// 60 B header (20 B fixed + 40 B max options) plus up to
// ipv4MaxPayload (1480 B) of payload. Worst case ~1540 B —
// comfortably under the Ring-0 16 KiB stack ceiling.
const tcpScratchSize = tcpHeaderMaxSize + ipv4MaxPayload

// tcpSendSegment composes a TCP segment, fills the pseudo-
// header checksum, and hands it to ipv4Send. The caller
// supplies the flags, optional options blob (nil for pure ACK
// or data segments; 4-byte MSS option for SYN / SYN|ACK), and
// optional payload. Returns false on oversize or TX failure.
//
// The caller must NOT hold tcbTableLock; ipv4Send may block on
// arpResolve and eventually acquires netBufLock (rank 5) —
// that inversion is illegal if rank 9 is held. State-machine
// handlers must release rank 9 before calling this.
func tcpSendSegment(t *TCB, flags uint8, options, payload []byte) bool {
	optLen := len(options)
	if optLen%4 != 0 || optLen > 40 {
		return false
	}
	total := tcpHeaderMinSize + optLen + len(payload)
	if total > tcpScratchSize {
		return false
	}
	var buf [tcpScratchSize]byte
	n := tcpBuildSegment(
		buf[:total],
		t.localPort, t.remotePort,
		t.sndNxt, t.rcvNxt,
		flags,
		uint16(t.rcvWnd),
		options, payload,
	)
	if n == 0 {
		return false
	}
	tcpComputeAndSetChecksum(ourIP, t.remoteIP, buf[:n])
	return ipv4Send(ipProtoTCP, t.remoteIP, buf[:n])
}

// tcpHandle is the RX dispatcher for TCP segments, called from
// ipv4Handle's protocol switch (src/ipv4.go). Full state-
// machine dispatch lands in the next Phase TCP-1 commit; this
// interim version silently drops segments (no RST yet).
func tcpHandle(hdr IPv4Header, inner []byte) {
	_ = hdr
	_ = inner
}
