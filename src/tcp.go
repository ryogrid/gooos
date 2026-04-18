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

// tcpMaxListeners caps concurrent passive-open ports.
// tcpAcceptQueueDepth caps pending + accepted connections per
// listener. See TD2 in impldoc/net_tcp_overview.md §5.
const (
	tcpMaxListeners     = 4
	tcpAcceptQueueDepth = 8
)

// tcpListener tracks one passive-open port. Protected by
// tcpListenLock (rank 10). See net_tcp_state_machine.md §6.
type tcpListener struct {
	port    uint16
	active  bool
	owner   int // pid; -1 = kernel-internal

	// TCBs in SYN_RECEIVED, waiting for the third-handshake ACK.
	pending  [tcpAcceptQueueDepth]*TCB
	nPending int

	// TCBs in ESTABLISHED (or beyond), waiting for accept().
	accept  [tcpAcceptQueueDepth]*TCB
	nAccept int
}

var (
	tcpListeners  [tcpMaxListeners]tcpListener
	tcpListenLock Spinlock // lock ordering rank 10
)

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

	// Listener that spawned us (non-nil only for passive-open
	// TCBs). Used to splice into pending → accept queues.
	listener *tcpListener

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

	// Retransmission queue + RTO state (Phase TCP-2 item 2).
	// See src/tcp_retx.go and net_tcp_timers_and_rtt.md §3.
	// bufBase is the sequence number corresponding to
	// txBuf.head — i.e. the seq of the oldest byte still in the
	// send ring. Lets retransmit descriptors carry a seq-based
	// offset without an absolute pointer.
	retxQ               tcpRetxQueue
	bufBase             uint32
	rtoTicks            uint32 // current RTO in PIT ticks
	rtoDeadline         uint64 // pitTicks at which RTO fires; 0 = inactive
	xmitCountHead       uint8  // retransmits of head-of-queue
	rtoGoroutineRunning bool

	// rtoFastRetx: true when the next tcpRTOFire was forced by
	// fast-retransmit (3 dup ACKs). In that case tcpRTOFire
	// skips the RFC 5681 cwnd-to-1-MSS collapse — CC already
	// set cwnd = ssthresh + 3*mss via tcpCCOnDupAck.
	rtoFastRetx bool

	// RTT estimator state (RFC 6298 — see src/tcp_rtt.go).
	// srttTicks is scaled ×8, rttvarTicks ×4; rttInitialized
	// distinguishes "never sampled" (use init path) from
	// "already have SRTT" (use update path).
	srttTicks      uint32
	rttvarTicks    uint32
	rttInitialized bool

	// TIME_WAIT deadline (pitTicks). Populated when entering
	// TIME_WAIT; consumed by the RTO/timer scanner which calls
	// tcbFree on expiry. Phase TCP-2 item 5.
	timeWaitDeadline uint64

	// Last value we advertised as our receive window. Used by
	// the SWS-avoidance helper to stop announcing tiny window
	// growth. Phase TCP-3 item 1.
	lastAdvWin uint32

	// Persist-timer deadline. Armed when the peer advertises a
	// zero send window and we still have data queued for TX.
	// Fires a 1-byte window probe per RFC 1122 §4.2.2.17.
	// Phase TCP-3 item 3.
	persistDeadline uint64
	persistTicks    uint32 // current persist interval (exponential back-off)

	// Congestion control (RFC 5681). cwnd / ssthresh in bytes;
	// cwndAccum is the fractional-byte accumulator for
	// congestion avoidance's per-ACK increment; dupAcks counts
	// consecutive duplicate ACKs for fast retransmit.
	// Phase TCP-4.
	cwnd      uint32
	ssthresh  uint32
	cwndAccum uint32
	dupAcks   uint8

	// Delayed-ACK deadline. Armed on in-order data receive;
	// cleared when a data-bearing segment we send piggybacks
	// the ACK (or when we emit an immediate ACK). Phase TCP-3
	// item 4. Current state machine still emits immediate ACKs
	// (correctness-first); the field is populated but the
	// "hold ACK up to 200 ms" behaviour is dormant until the
	// echo server is refactored to stage outbound bytes in
	// txBuf.
	delackDeadline uint64

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
		tcpAdvertiseWin(t),
		options, payload,
	)
	if n == 0 {
		return false
	}
	tcpComputeAndSetChecksum(ourIP, t.remoteIP, buf[:n])
	return ipv4Send(ipProtoTCP, t.remoteIP, buf[:n])
}

// --- listener table helpers (caller must hold tcpListenLock) ---

// tcpListenerAllocLocked claims a free listener slot for `port`
// and pid `owner`. Returns nil if the port is already taken or
// the table is full. Caller MUST hold tcpListenLock.
func tcpListenerAllocLocked(port uint16, owner int) *tcpListener {
	for i := 0; i < tcpMaxListeners; i++ {
		l := &tcpListeners[i]
		if l.active && l.port == port {
			return nil
		}
	}
	for i := 0; i < tcpMaxListeners; i++ {
		l := &tcpListeners[i]
		if !l.active {
			l.port = port
			l.owner = owner
			l.active = true
			l.nPending = 0
			l.nAccept = 0
			return l
		}
	}
	return nil
}

// tcpListenerLookupLocked returns the active listener bound to
// `port`, or nil. Caller MUST hold tcpListenLock.
func tcpListenerLookupLocked(port uint16) *tcpListener {
	for i := 0; i < tcpMaxListeners; i++ {
		l := &tcpListeners[i]
		if l.active && l.port == port {
			return l
		}
	}
	return nil
}

// tcpListenerPushPending appends a SYN_RECEIVED TCB to the
// listener's pending queue. Returns false if the queue is full.
// Caller MUST hold tcpListenLock.
func tcpListenerPushPending(l *tcpListener, t *TCB) bool {
	if l.nPending+l.nAccept >= tcpAcceptQueueDepth {
		return false
	}
	l.pending[l.nPending] = t
	l.nPending++
	return true
}

// tcpListenerPromote splices a TCB from pending → accept on the
// third-handshake ACK. Returns false if the TCB isn't in
// pending. Caller MUST hold tcpListenLock.
func tcpListenerPromote(l *tcpListener, t *TCB) bool {
	idx := -1
	for i := 0; i < l.nPending; i++ {
		if l.pending[i] == t {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	// Shift left.
	for i := idx; i < l.nPending-1; i++ {
		l.pending[i] = l.pending[i+1]
	}
	l.pending[l.nPending-1] = nil
	l.nPending--
	// Append to accept.
	if l.nAccept >= tcpAcceptQueueDepth {
		return false
	}
	l.accept[l.nAccept] = t
	l.nAccept++
	return true
}

// tcpListenerRemove drops a TCB from either queue on a reset
// path. Caller MUST hold tcpListenLock.
func tcpListenerRemove(l *tcpListener, t *TCB) {
	for i := 0; i < l.nPending; i++ {
		if l.pending[i] == t {
			for j := i; j < l.nPending-1; j++ {
				l.pending[j] = l.pending[j+1]
			}
			l.pending[l.nPending-1] = nil
			l.nPending--
			return
		}
	}
	for i := 0; i < l.nAccept; i++ {
		if l.accept[i] == t {
			for j := i; j < l.nAccept-1; j++ {
				l.accept[j] = l.accept[j+1]
			}
			l.accept[l.nAccept-1] = nil
			l.nAccept--
			return
		}
	}
}

// --- state-machine dispatch ---

// segLen returns the RFC 793 "sequence number space" consumed
// by a segment: payload bytes plus 1 for each of SYN/FIN.
func segLen(flags uint8, payloadLen int) uint32 {
	n := uint32(payloadLen)
	if flags&tcpFlagSYN != 0 {
		n++
	}
	if flags&tcpFlagFIN != 0 {
		n++
	}
	return n
}

// tcpHandle is the RX dispatcher for TCP segments, called from
// ipv4Handle's protocol switch (src/ipv4.go). Parses the
// segment, verifies the checksum, looks up the owning TCB (or
// listener), and dispatches to a per-state handler. Segments
// that don't match any TCB or listener are silently dropped at
// this stage; RST-on-no-match lands in Phase TCP-1 item 7
// (tcpRejectSegment). Runs from the netRxLoop goroutine, not
// from the e1000 ISR itself — allocation is allowed.
func tcpHandle(hdr IPv4Header, inner []byte) {
	if len(inner) < tcpHeaderMinSize {
		return
	}
	if !tcpChecksumVerify(hdr.SrcIP, hdr.DstIP, inner) {
		return
	}
	h, payload, ok := tcpParse(inner)
	if !ok {
		return
	}
	// Look up an existing TCB first (handles retransmits of the
	// initial SYN and all post-handshake segments).
	t := tcbLookup(hdr.DstIP, h.DstPort, hdr.SrcIP, h.SrcPort)
	if t != nil {
		tcpDispatchToTCB(t, h, payload)
		return
	}
	// No TCB — try the listener table for passive open on SYN.
	if h.Flags&tcpFlagSYN != 0 && h.Flags&tcpFlagACK == 0 {
		tcpTryPassiveOpen(hdr, h, payload)
		return
	}
	// No match and not a passive-open SYN. RFC 793 §3.4: reply
	// with RST unless the incoming segment already has RST.
	if h.Flags&tcpFlagRST == 0 {
		tcpSendReset(
			hdr.DstIP, h.DstPort,
			hdr.SrcIP, h.SrcPort,
			h.Ack, h.Seq, h.Flags, len(payload),
		)
	}
}

// tcpTryPassiveOpen handles an incoming SYN against the listener
// table. Allocates a fresh TCB, negotiates MSS from the SYN's
// options, sends SYN|ACK, and parks the TCB on the listener's
// pending queue.
func tcpTryPassiveOpen(hdr IPv4Header, h TCPHeader, payload []byte) {
	_ = payload // no payload expected on a pure SYN

	// Find listener under tcpListenLock.
	lflags := tcpListenLock.Acquire()
	l := tcpListenerLookupLocked(h.DstPort)
	if l == nil {
		tcpListenLock.Release(lflags)
		tcpSendReset(
			hdr.DstIP, h.DstPort,
			hdr.SrcIP, h.SrcPort,
			h.Ack, h.Seq, h.Flags, len(payload),
		)
		return
	}
	// Reject if pending+accept already at depth cap.
	if l.nPending+l.nAccept >= tcpAcceptQueueDepth {
		tcpListenLock.Release(lflags)
		tcpSendReset(
			hdr.DstIP, h.DstPort,
			hdr.SrcIP, h.SrcPort,
			h.Ack, h.Seq, h.Flags, len(payload),
		)
		return
	}
	tcpListenLock.Release(lflags)

	// Allocate TCB.
	t := tcbAlloc(hdr.DstIP, h.DstPort, hdr.SrcIP, h.SrcPort)
	if t == nil {
		// TCB-table exhaustion → send RST so the peer can retry.
		tcpSendReset(
			hdr.DstIP, h.DstPort,
			hdr.SrcIP, h.SrcPort,
			h.Ack, h.Seq, h.Flags, len(payload),
		)
		return
	}

	// Parse peer options for MSS.
	peerMSS := tcpDefaultMSS
	if h.OptLen > 0 {
		peerMSS, _ = tcpParseOptions(h.Options[:h.OptLen])
	}

	// Initialise TCB under tcbTableLock.
	iflags := tcbTableLock.Acquire()
	t.state = tcpStateSynReceived
	t.iss = isnNext()
	t.irs = h.Seq
	t.rcvNxt = h.Seq + 1 // past the SYN
	t.rcvWnd = uint32(tcpRxBufSize)
	t.sndUna = t.iss
	t.sndNxt = t.iss + 1 // past our SYN
	t.sndWnd = uint32(h.Window)
	t.sndWl1 = h.Seq
	t.sndWl2 = 0
	t.mssLocal = tcpDefaultMSS
	t.mssPeer = peerMSS
	if peerMSS < t.mssLocal {
		t.mssEff = peerMSS
	} else {
		t.mssEff = t.mssLocal
	}
	t.listener = l
	tcbTableLock.Release(iflags)

	// Attach to listener's pending queue.
	lflags = tcpListenLock.Acquire()
	if !tcpListenerPushPending(l, t) {
		tcpListenLock.Release(lflags)
		tcbFree(t)
		return
	}
	tcpListenLock.Release(lflags)

	// Build outbound SYN|ACK with the MSS option. sndNxt already
	// sits one past iss, but the SYN|ACK itself occupies iss —
	// temporarily roll back for the send.
	iflags = tcbTableLock.Acquire()
	origSndNxt := t.sndNxt
	t.sndNxt = t.iss
	tcbTableLock.Release(iflags)

	var mssOpt [4]byte
	tcpBuildMSSOption(mssOpt[:], t.mssLocal)
	ok := tcpSendSegment(t, tcpFlagSYN|tcpFlagACK, mssOpt[:], nil)

	iflags = tcbTableLock.Acquire()
	t.sndNxt = origSndNxt // restore past-SYN cursor
	if ok {
		retxPush(t, tcpRetxEntry{
			seq:       t.iss,
			endSeq:    t.iss + 1,
			flags:     tcpFlagSYN | tcpFlagACK,
			sentTicks: pitTicks,
		})
		tcpArmRTO(t)
	}
	tcbTableLock.Release(iflags)

	if !ok {
		// SYN|ACK TX failed; unwind.
		lflags = tcpListenLock.Acquire()
		tcpListenerRemove(l, t)
		tcpListenLock.Release(lflags)
		tcbFree(t)
	}
}

// tcpDispatchToTCB routes a parsed segment to the per-state
// handler. The TCB pointer stays valid for the duration of the
// call (no other goroutine frees it while we hold the segment
// in hand; worst case a concurrent syscall observes a stale
// state enum, which is benign because each handler re-checks
// under tcbTableLock before mutating).
func tcpDispatchToTCB(t *TCB, h TCPHeader, payload []byte) {
	switch t.state {
	case tcpStateSynSent:
		tcpHandleSynSent(t, h, payload)
	case tcpStateSynReceived:
		tcpHandleSynReceived(t, h, payload)
	case tcpStateEstablished:
		tcpHandleEstablished(t, h, payload)
	case tcpStateCloseWait:
		tcpHandleCloseWait(t, h, payload)
	case tcpStateLastAck:
		tcpHandleLastAck(t, h, payload)
	case tcpStateFinWait1:
		tcpHandleFinWait1(t, h, payload)
	case tcpStateFinWait2:
		tcpHandleFinWait2(t, h, payload)
	case tcpStateClosing:
		tcpHandleClosing(t, h, payload)
	case tcpStateTimeWait:
		tcpHandleTimeWait(t, h, payload)
	default:
		// tcpStateClosed, tcpStateListen: not dispatched here.
	}
}

// tcpHandleSynReceived: SYN_RECEIVED + ACK (of our SYN|ACK) →
// ESTABLISHED. Moves TCB from pending to accept queue. Other
// incoming segments here are ignored for now; item 7 refines.
func tcpHandleSynReceived(t *TCB, h TCPHeader, payload []byte) {
	if h.Flags&tcpFlagACK == 0 {
		return
	}
	// Validate ACK covers our SYN.
	if h.Ack != t.sndNxt {
		return
	}
	iflags := tcbTableLock.Acquire()
	t.state = tcpStateEstablished
	t.sndUna = h.Ack
	t.sndWnd = uint32(h.Window)
	t.sndWl1 = h.Seq
	t.sndWl2 = h.Ack
	tcpCCInit(t) // RFC 5681 §3.1 — seed cwnd / ssthresh
	// Pop the SYN|ACK descriptor from retxQ and feed the RTT
	// estimator (Karn's rule: only pristine descriptors).
	_, oldestSent, anyPristine := retxAckTo(t, h.Ack)
	tcpRTTSample(t, oldestSent, anyPristine)
	if t.retxQ.n == 0 {
		t.rtoDeadline = 0
	} else {
		tcpArmRTO(t)
	}
	l := t.listener
	tcbTableLock.Release(iflags)

	if l != nil {
		lflags := tcpListenLock.Acquire()
		tcpListenerPromote(l, t)
		tcpListenLock.Release(lflags)
	}
	// If the third-handshake segment also carried data, run the
	// ESTABLISHED-state receive path over it.
	if len(payload) > 0 || h.Flags&tcpFlagFIN != 0 {
		tcpHandleEstablished(t, h, payload)
	}
}

// tcpHandleEstablished: ESTABLISHED data + ACK handling. Data
// is copied into rxBuf and an ACK is sent immediately (delayed-
// ACK arrives in Phase TCP-3). FIN transitions into CLOSE_WAIT.
func tcpHandleEstablished(t *TCB, h TCPHeader, payload []byte) {
	// Only accept in-order data (out-of-order dropped per v1
	// non-goal in overview §1.2).
	iflags := tcbTableLock.Acquire()
	if h.Seq != t.rcvNxt {
		// Out-of-order — send a pure ACK to help peer recover.
		tcbTableLock.Release(iflags)
		tcpSendPureACK(t)
		return
	}
	// Fast-retransmit path: a pure duplicate ACK bumps dupAcks;
	// the 3rd one triggers retransmission of the head segment
	// before RTO fires. Force the next scanner pass to fire
	// tcpRTOFire on this TCB by zeroing rtoDeadline backward.
	fastRetx := false
	if tcpIsDuplicateACK(t, h, len(payload)) {
		fastRetx = tcpCCOnDupAck(t)
		if fastRetx {
			// Force the RTO scanner to retransmit on the next
			// scan pass (within 50 ms). Using the scanner avoids
			// an inline retransmit while we hold tcbTableLock —
			// tcpRTOFire releases the lock before calling
			// ipv4Send, which is the right ordering. The
			// rtoFastRetx flag tells tcpRTOFire to skip its
			// cwnd-to-1-MSS collapse — CC already picked the
			// right cwnd via tcpCCOnDupAck.
			t.rtoDeadline = 1 // any past tick
			t.xmitCountHead = 0
			t.rtoFastRetx = true
		}
	} else {
		tcpAckUpdate(t, h) // handles non-dup ACKs
	}
	// Accept in-order payload bytes into rxBuf.
	if len(payload) > 0 {
		n := t.rxBuf.rbWrite(payload)
		t.rcvNxt += uint32(n)
		// rcvWnd shrinks by what we just buffered.
		if t.rcvWnd > uint32(n) {
			t.rcvWnd -= uint32(n)
		} else {
			t.rcvWnd = 0
		}
	}
	// FIN consumes one sequence number.
	fin := h.Flags&tcpFlagFIN != 0
	if fin {
		t.rcvNxt++
		t.state = tcpStateCloseWait
	}
	tcbTableLock.Release(iflags)

	// Acknowledge. This is a pure ACK (no payload of our own
	// yet — the echo goroutine in item 8 sends data-bearing
	// segments).
	tcpSendPureACK(t)
}

// tcpHandleCloseWait: peer has already FIN'd; we're waiting for
// local close. Retransmitted data / FIN is ACKed; nothing else.
func tcpHandleCloseWait(t *TCB, h TCPHeader, payload []byte) {
	_ = payload
	if h.Flags&tcpFlagFIN != 0 {
		// Peer retransmit of FIN — just re-ACK rcvNxt.
		tcpSendPureACK(t)
	}
}

// tcpHandleLastAck: waiting for ACK of our FIN. On match, free
// the TCB. Other segments ignored.
func tcpHandleLastAck(t *TCB, h TCPHeader, payload []byte) {
	_ = payload
	if h.Flags&tcpFlagACK != 0 && h.Ack == t.sndNxt {
		tcbFree(t)
	}
}

// tcpSendPureACK emits an ACK segment with no payload and no
// options. Used for rcvNxt advance + out-of-order rejection +
// FIN acknowledgement. Caller must NOT hold tcbTableLock.
func tcpSendPureACK(t *TCB) bool {
	return tcpSendSegment(t, tcpFlagACK, nil, nil)
}

// tcpSendReset emits a stateless RST for a segment that has no
// matching TCB. Per RFC 793 §3.4:
//   - incoming RST=1: caller must NOT invoke this (drop silently).
//   - incoming ACK=1: reply carries RST only, seq=inAck.
//   - incoming ACK=0: reply carries RST|ACK, seq=0,
//                     ack=inSeq+segLen.
// srcIP/srcPort are the local endpoint (i.e. the incoming
// segment's DstIP/DstPort) and dstIP/dstPort are the peer's.
func tcpSendReset(srcIP uint32, srcPort uint16,
	dstIP uint32, dstPort uint16,
	inAck, inSeq uint32, inFlags uint8, inPayloadLen int) bool {
	var seq, ack uint32
	var flags uint8
	if inFlags&tcpFlagACK != 0 {
		flags = tcpFlagRST
		seq = inAck
		ack = 0
	} else {
		flags = tcpFlagRST | tcpFlagACK
		seq = 0
		ack = inSeq + segLen(inFlags, inPayloadLen)
	}
	var buf [tcpHeaderMinSize]byte
	n := tcpBuildSegment(
		buf[:],
		srcPort, dstPort,
		seq, ack,
		flags,
		0, // zero window on a stateless RST
		nil, nil,
	)
	if n == 0 {
		return false
	}
	tcpComputeAndSetChecksum(srcIP, dstIP, buf[:n])
	return ipv4Send(ipProtoTCP, dstIP, buf[:n])
}

// seqLT / seqLE are RFC 793 §3.3 modular sequence-number
// comparisons. Interpret the 32-bit sequence space as a circle
// and compare via signed subtraction.
func seqLT(a, b uint32) bool { return int32(a-b) < 0 }
func seqLE(a, b uint32) bool { return int32(a-b) <= 0 }

// --- kernel echo server (Phase TCP-1 item 8) ---

// tcpEchoListenPort is the fixed port for the kernel TCP echo
// service. Matches the Makefile run-net hostfwd that maps
// host 10080 → guest 8080.
const tcpEchoListenPort uint16 = 8080

// tcpEchoPollTicks is how often the echo goroutine checks for
// work. At 100 Hz PIT this is ~50 ms — coarse enough to keep
// CPU use low, fine enough that small-message RTT stays under
// 100 ms under QEMU user-mode. Rewire to a channel-based wake
// when flow-control / persist timers land (Phase TCP-3).
const tcpEchoPollTicks uint64 = 5

// tcpTimeWaitTicks is the 2*MSL TIME_WAIT dwell. 60 s per
// design doc TD5 in net_tcp_overview.md §5; shorter than RFC
// 793's 4-minute default to keep the 16-slot TCB table usable
// under churn.
const tcpTimeWaitTicks uint64 = 6000 // 60 s at 100 Hz PIT

// --- FIN path handlers (Phase TCP-2 item 4) ---

// tcpClose initiates a graceful close. Called by whichever code
// path "owns" the TCB (the kernel echo server, future
// sys_close). Sends FIN|ACK and transitions the state machine
// accordingly:
//   ESTABLISHED → FIN_WAIT_1
//   CLOSE_WAIT  → LAST_ACK  (peer already FIN'd; this is the
//                            half-close completion from our side)
// Caller must NOT hold tcbTableLock.
func tcpClose(t *TCB) bool {
	iflags := tcbTableLock.Acquire()
	switch t.state {
	case tcpStateEstablished:
		t.state = tcpStateFinWait1
	case tcpStateCloseWait:
		t.state = tcpStateLastAck
	default:
		tcbTableLock.Release(iflags)
		return false // nothing to close
	}
	tcbTableLock.Release(iflags)

	ok := tcpSendSegment(t, tcpFlagFIN|tcpFlagACK, nil, nil)
	if !ok {
		return false
	}
	iflags = tcbTableLock.Acquire()
	// FIN consumes 1 seq.
	finSeq := t.sndNxt
	t.sndNxt++
	retxPush(t, tcpRetxEntry{
		seq:       finSeq,
		endSeq:    finSeq + 1,
		flags:     tcpFlagFIN | tcpFlagACK,
		sentTicks: pitTicks,
	})
	tcpArmRTO(t)
	tcbTableLock.Release(iflags)
	return true
}

// tcpHandleFinWait1: we've sent FIN and are waiting for its ACK
// and/or the peer's FIN.
//   ACK of our FIN alone  → FIN_WAIT_2
//   FIN alone             → CLOSING (our FIN not yet acked)
//   FIN + ACK of our FIN  → TIME_WAIT directly
func tcpHandleFinWait1(t *TCB, h TCPHeader, payload []byte) {
	iflags := tcbTableLock.Acquire()
	// Accept in-order data riding along.
	if h.Seq == t.rcvNxt && len(payload) > 0 {
		n := t.rxBuf.rbWrite(payload)
		t.rcvNxt += uint32(n)
		if t.rcvWnd > uint32(n) {
			t.rcvWnd -= uint32(n)
		} else {
			t.rcvWnd = 0
		}
	}
	// sndUna / sndWnd update + detect ACK-of-our-FIN.
	ackOfOurFin := tcpAckUpdate(t, h)
	// Detect peer FIN (only if in-order).
	peerFin := false
	if h.Flags&tcpFlagFIN != 0 &&
		h.Seq+uint32(len(payload)) == t.rcvNxt {
		t.rcvNxt++
		peerFin = true
	}
	switch {
	case ackOfOurFin && peerFin:
		t.state = tcpStateTimeWait
		t.timeWaitDeadline = pitTicks + tcpTimeWaitTicks
	case ackOfOurFin:
		t.state = tcpStateFinWait2
	case peerFin:
		t.state = tcpStateClosing
	}
	tcbTableLock.Release(iflags)

	// Always ACK (of data / of their FIN).
	tcpSendPureACK(t)
}

// tcpHandleFinWait2: our FIN has been ACKed; waiting for peer FIN.
func tcpHandleFinWait2(t *TCB, h TCPHeader, payload []byte) {
	iflags := tcbTableLock.Acquire()
	// Accept rolling ACK / window updates (no data left on our
	// side, but the peer may still send window-update segments).
	tcpAckUpdate(t, h)
	if h.Seq == t.rcvNxt && len(payload) > 0 {
		n := t.rxBuf.rbWrite(payload)
		t.rcvNxt += uint32(n)
		if t.rcvWnd > uint32(n) {
			t.rcvWnd -= uint32(n)
		} else {
			t.rcvWnd = 0
		}
	}
	peerFin := false
	if h.Flags&tcpFlagFIN != 0 &&
		h.Seq+uint32(len(payload)) == t.rcvNxt {
		t.rcvNxt++
		peerFin = true
	}
	if peerFin {
		t.state = tcpStateTimeWait
		t.timeWaitDeadline = pitTicks + tcpTimeWaitTicks
	}
	tcbTableLock.Release(iflags)
	tcpSendPureACK(t)
}

// tcpHandleClosing: we and peer both sent FIN; waiting for ACK
// of our FIN.
func tcpHandleClosing(t *TCB, h TCPHeader, payload []byte) {
	_ = payload
	iflags := tcbTableLock.Acquire()
	ackOfOurFin := tcpAckUpdate(t, h)
	if ackOfOurFin {
		t.state = tcpStateTimeWait
		t.timeWaitDeadline = pitTicks + tcpTimeWaitTicks
	}
	tcbTableLock.Release(iflags)
}

// tcpHandleTimeWait: waiting for 2*MSL (60 s in v1) before
// freeing the slot. Peer retransmits of FIN are re-ACKed and
// reset the deadline (RFC 793 §3.5).
func tcpHandleTimeWait(t *TCB, h TCPHeader, payload []byte) {
	_ = payload
	if h.Flags&tcpFlagFIN != 0 {
		iflags := tcbTableLock.Acquire()
		t.timeWaitDeadline = pitTicks + tcpTimeWaitTicks
		tcbTableLock.Release(iflags)
		tcpSendPureACK(t)
	}
}

// tcpHandleSynSent: SYN_SENT + SYN|ACK (valid ACK of our SYN) →
// ESTABLISHED; emit final ACK. SYN_SENT + RST → free. Simul-
// open (bare SYN from peer) is rejected with RST per v1
// simplification (net_tcp_state_machine.md §4).
func tcpHandleSynSent(t *TCB, h TCPHeader, payload []byte) {
	// RST on an unacceptable ACK in SYN_SENT is legal; follow
	// RFC 793 §3.9 check that any ACK present covers iss..sndNxt.
	if h.Flags&tcpFlagACK != 0 {
		if !(seqLE(t.sndUna+1, h.Ack) && seqLE(h.Ack, t.sndNxt)) {
			// Bogus ACK; drop (we could RST but keep it simple).
			if h.Flags&tcpFlagRST == 0 {
				tcpSendReset(
					t.localIP, t.localPort,
					t.remoteIP, t.remotePort,
					h.Ack, h.Seq, h.Flags, len(payload),
				)
			}
			return
		}
	}
	if h.Flags&tcpFlagRST != 0 {
		tcbFree(t)
		return
	}
	// Simultaneous open (SYN alone, no ACK): v1 rejects with RST.
	if h.Flags&tcpFlagSYN != 0 && h.Flags&tcpFlagACK == 0 {
		tcpSendReset(
			t.localIP, t.localPort,
			t.remoteIP, t.remotePort,
			h.Ack, h.Seq, h.Flags, len(payload),
		)
		tcbFree(t)
		return
	}
	// Expect SYN|ACK.
	if h.Flags&(tcpFlagSYN|tcpFlagACK) != (tcpFlagSYN | tcpFlagACK) {
		return
	}

	// Parse peer MSS from options.
	peerMSS := tcpDefaultMSS
	if h.OptLen > 0 {
		peerMSS, _ = tcpParseOptions(h.Options[:h.OptLen])
	}

	iflags := tcbTableLock.Acquire()
	t.irs = h.Seq
	t.rcvNxt = h.Seq + 1 // past peer SYN
	t.sndUna = h.Ack
	t.sndWnd = uint32(h.Window)
	t.sndWl1 = h.Seq
	t.sndWl2 = h.Ack
	t.mssPeer = peerMSS
	if peerMSS < t.mssLocal {
		t.mssEff = peerMSS
	} else {
		t.mssEff = t.mssLocal
	}
	t.state = tcpStateEstablished
	tcpCCInit(t)
	_, oldestSent, anyPristine := retxAckTo(t, h.Ack)
	tcpRTTSample(t, oldestSent, anyPristine)
	if t.retxQ.n == 0 {
		t.rtoDeadline = 0
	} else {
		tcpArmRTO(t)
	}
	tcbTableLock.Release(iflags)

	// Emit final ACK of 3WHS.
	tcpSendPureACK(t)

	// If the SYN|ACK also carried data (unusual), deliver it via
	// the ESTABLISHED handler so rxBuf fills correctly.
	if len(payload) > 0 || h.Flags&tcpFlagFIN != 0 {
		// Re-run the ESTABLISHED handler against the same segment
		// — it will see seq == rcvNxt and accept.
		tcpHandleEstablished(t, h, payload)
	}
}

// --- active open ---

// tcpEphemeralBase/Top bound the ephemeral local-port range.
// 16-port window matches the 16 max TCBs and avoids fighting
// with udpBindings (UDP uses a separate port space).
const (
	tcpEphemeralBase uint16 = 49152
	tcpEphemeralTop  uint16 = 49167
)

// tcpAllocEphemeralPort finds a local port not currently in use
// by any active TCB. Returns 0 if the range is exhausted.
// Caller must NOT hold tcbTableLock.
func tcpAllocEphemeralPort() uint16 {
	flags := tcbTableLock.Acquire()
	defer tcbTableLock.Release(flags)
	for p := tcpEphemeralBase; p <= tcpEphemeralTop; p++ {
		inUse := false
		for i := 0; i < tcbMax; i++ {
			if tcbTable[i].active && tcbTable[i].localPort == p {
				inUse = true
				break
			}
		}
		if !inUse {
			return p
		}
	}
	return 0
}

// tcpActiveConnect initiates a client-side 3-way handshake to
// remoteIP:remotePort. Allocates a TCB in SYN_SENT, emits the
// initial SYN, and returns the TCB pointer. The caller polls
// t.state for ESTABLISHED (or CLOSED on failure). Retransmission
// of a lost SYN lands in Phase TCP-2 item 2; for now the SYN
// is sent once and the peer is expected to answer.
//
// Returns nil if the ephemeral range is exhausted, the TCB
// table is full, or the initial SYN cannot be transmitted.
func tcpActiveConnect(remoteIP uint32, remotePort uint16) *TCB {
	lp := tcpAllocEphemeralPort()
	if lp == 0 {
		return nil
	}
	t := tcbAlloc(ourIP, lp, remoteIP, remotePort)
	if t == nil {
		return nil
	}
	iflags := tcbTableLock.Acquire()
	t.state = tcpStateSynSent
	t.iss = isnNext()
	t.sndUna = t.iss
	t.sndNxt = t.iss + 1 // past our SYN
	t.rcvWnd = uint32(tcpRxBufSize)
	t.mssLocal = tcpDefaultMSS
	t.mssEff = tcpDefaultMSS
	tcbTableLock.Release(iflags)

	// Emit SYN with MSS option. Roll sndNxt back to iss for this
	// send so the SYN carries iss as its seq.
	iflags = tcbTableLock.Acquire()
	origSndNxt := t.sndNxt
	t.sndNxt = t.iss
	tcbTableLock.Release(iflags)

	var mssOpt [4]byte
	tcpBuildMSSOption(mssOpt[:], t.mssLocal)
	ok := tcpSendSegment(t, tcpFlagSYN, mssOpt[:], nil)

	iflags = tcbTableLock.Acquire()
	t.sndNxt = origSndNxt
	if ok {
		retxPush(t, tcpRetxEntry{
			seq:       t.iss,
			endSeq:    t.iss + 1,
			flags:     tcpFlagSYN,
			sentTicks: pitTicks,
		})
		tcpArmRTO(t)
	}
	tcbTableLock.Release(iflags)

	if !ok {
		tcbFree(t)
		return nil
	}
	return t
}

// tcpStateName returns a short human-readable name for a state.
// Used by netDiag; the strings are static literals so the ISR
// lint does not flag any concat on the hot path.
func tcpStateName(s tcbState) string {
	switch s {
	case tcpStateClosed:
		return "CLOSED"
	case tcpStateListen:
		return "LISTEN"
	case tcpStateSynSent:
		return "SYN_SENT"
	case tcpStateSynReceived:
		return "SYN_RECEIVED"
	case tcpStateEstablished:
		return "ESTABLISHED"
	case tcpStateFinWait1:
		return "FIN_WAIT_1"
	case tcpStateFinWait2:
		return "FIN_WAIT_2"
	case tcpStateCloseWait:
		return "CLOSE_WAIT"
	case tcpStateClosing:
		return "CLOSING"
	case tcpStateLastAck:
		return "LAST_ACK"
	case tcpStateTimeWait:
		return "TIME_WAIT"
	}
	return "?"
}

// tcpDiag prints the listener table and active TCBs. Called
// from netDiag (src/net.go). Takes the TCP locks in canonical
// order (tcbTableLock rank 9 then tcpListenLock rank 10) and
// releases them before serial output so serial write never
// runs under either.
func tcpDiag() {
	serialPrintln("TCP listeners:")
	// Snapshot listeners.
	var lPorts [tcpMaxListeners]uint16
	var lPending [tcpMaxListeners]int
	var lAccept [tcpMaxListeners]int
	var lActive [tcpMaxListeners]bool
	flags := tcpListenLock.Acquire()
	for i := 0; i < tcpMaxListeners; i++ {
		lActive[i] = tcpListeners[i].active
		lPorts[i] = tcpListeners[i].port
		lPending[i] = tcpListeners[i].nPending
		lAccept[i] = tcpListeners[i].nAccept
	}
	tcpListenLock.Release(flags)
	anyL := false
	for i := 0; i < tcpMaxListeners; i++ {
		if !lActive[i] {
			continue
		}
		serialPrintln("  port=" + utoa(uint64(lPorts[i])) +
			" pending=" + utoa(uint64(lPending[i])) +
			" accept=" + utoa(uint64(lAccept[i])))
		anyL = true
	}
	if !anyL {
		serialPrintln("  (none)")
	}

	serialPrintln("TCP TCBs:")
	// Snapshot TCBs.
	type tcbSnap struct {
		active                             bool
		localIP, remoteIP                  uint32
		localPort, remotePort              uint16
		state                              tcbState
		rxLen, txLen                       int
		sndUna, sndNxt, rcvNxt             uint32
	}
	var snaps [tcbMax]tcbSnap
	flags = tcbTableLock.Acquire()
	for i := 0; i < tcbMax; i++ {
		t := &tcbTable[i]
		snaps[i].active = t.active
		if !t.active {
			continue
		}
		snaps[i].localIP = t.localIP
		snaps[i].remoteIP = t.remoteIP
		snaps[i].localPort = t.localPort
		snaps[i].remotePort = t.remotePort
		snaps[i].state = t.state
		snaps[i].rxLen = t.rxBuf.rbLen()
		snaps[i].txLen = t.txBuf.rbLen()
		snaps[i].sndUna = t.sndUna
		snaps[i].sndNxt = t.sndNxt
		snaps[i].rcvNxt = t.rcvNxt
	}
	tcbTableLock.Release(flags)
	anyT := false
	for i := 0; i < tcbMax; i++ {
		if !snaps[i].active {
			continue
		}
		serialPrintln("  " + ipToString(snaps[i].localIP) + ":" +
			utoa(uint64(snaps[i].localPort)) + " <-> " +
			ipToString(snaps[i].remoteIP) + ":" +
			utoa(uint64(snaps[i].remotePort)) + " " +
			tcpStateName(snaps[i].state) +
			" rx=" + utoa(uint64(snaps[i].rxLen)) +
			" tx=" + utoa(uint64(snaps[i].txLen)))
		anyT = true
	}
	if !anyT {
		serialPrintln("  (none)")
	}
}

// tcpInit registers the kernel echo listener on port 8080,
// starts the RTO / TIME_WAIT scanner goroutine, and spawns the
// echo goroutine. Called from netInit after ARP is ready.
func tcpInit() {
	// Start the kernel-wide retransmission + timer scanner up
	// front so TIME_WAIT expiry always has a reaper even on
	// TCBs that never had an armed RTO of their own.
	tflags := tcbTableLock.Acquire()
	tcpStartRTOScanner()
	tcbTableLock.Release(tflags)

	flags := tcpListenLock.Acquire()
	l := tcpListenerAllocLocked(tcpEchoListenPort, -1)
	tcpListenLock.Release(flags)
	if l == nil {
		serialPrintln("TCP: failed to register echo listener")
		return
	}
	serialPrintln("TCP: listener port=8080 (kernel echo)")
	go tcpEchoServer()
}

// tcpEchoServer is the kernel-internal echo service for port
// 8080. Polls every TCB for bytes pending in rxBuf, sends them
// back as data segments, and drives the close handshake once
// the peer has FIN'd and our side has drained.
func tcpEchoServer() {
	var buf [tcpScratchSize]byte
	for {
		work := tcpEchoPass(buf[:])
		if !work {
			<-afterTicks(tcpEchoPollTicks)
		}
	}
}

// tcpEchoPass executes one scan across the TCB table, performing
// echo and close work. Returns true if any TCB was serviced —
// that signals the caller to loop immediately without sleeping
// so bursts drain quickly.
func tcpEchoPass(scratch []byte) bool {
	worked := false
	// Snapshot each candidate under the lock, but do the TX
	// outside (ipv4Send → netBufLock rank 5).
	for idx := 0; idx < tcbMax; idx++ {
		tflags := tcbTableLock.Acquire()
		t := &tcbTable[idx]
		if !t.active || t.localPort != tcpEchoListenPort {
			tcbTableLock.Release(tflags)
			continue
		}
		switch t.state {
		case tcpStateEstablished:
			if t.rxBuf.rbLen() == 0 {
				tcbTableLock.Release(tflags)
				continue
			}
			// Copy out up to mssEff bytes (or scratch capacity).
			limit := int(t.mssEff)
			if limit == 0 || limit > len(scratch) {
				limit = len(scratch)
			}
			n := t.rxBuf.rbRead(scratch[:limit])
			// Let rcvWnd recover — we just drained rxBuf.
			t.rcvWnd += uint32(n)
			if t.rcvWnd > uint32(tcpRxBufSize) {
				t.rcvWnd = uint32(tcpRxBufSize)
			}
			tcbTableLock.Release(tflags)

			if n == 0 {
				continue
			}
			ok := tcpSendSegment(t, tcpFlagACK|tcpFlagPSH, nil, scratch[:n])
			if ok {
				tflags = tcbTableLock.Acquire()
				t.sndNxt += uint32(n)
				tcbTableLock.Release(tflags)
				worked = true
			}
			// On TX failure the echoed bytes are dropped; Phase
			// TCP-2 retransmission will recover that.

		case tcpStateCloseWait:
			// Peer closed; if we've drained, send our FIN.
			if t.rxBuf.rbLen() != 0 {
				tcbTableLock.Release(tflags)
				continue
			}
			tcbTableLock.Release(tflags)
			ok := tcpSendSegment(t, tcpFlagFIN|tcpFlagACK, nil, nil)
			if ok {
				tflags = tcbTableLock.Acquire()
				t.sndNxt++ // FIN consumes 1 seq
				t.state = tcpStateLastAck
				tcbTableLock.Release(tflags)
				worked = true
			}

		default:
			tcbTableLock.Release(tflags)
		}
	}
	return worked
}
