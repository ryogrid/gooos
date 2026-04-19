package main

// TCP retransmission queue + RTO scanner.
//
// Data model per impldoc/net_tcp_segment_io.md §5:
//   Each in-flight segment is represented by a descriptor carrying
//   its seq range, flags, and (if applicable) an offset + length
//   into t.txBuf. On an RTO fire the descriptor is expanded back
//   into a wire segment via txBuf.rbPeek and retransmitted.
//
// Scope limitation for v1: the kernel echo server currently sends
// data directly from a stack buffer without pushing the bytes into
// t.txBuf first, so data retransmission is not yet wired. SYN and
// FIN control-segment retransmission IS wired — those are the two
// cases that matter for handshake / close correctness under loss.
// Data retransmission unlocks when the echo server (and later, the
// sys_tcp_send handler) route through t.txBuf.
//
// Lock ordering: retxQ fields live inside the TCB struct and are
// protected by tcbTableLock (rank 9). tcpTimerLock (rank 11) is
// reserved in spinlock.go for future fine-grained timer-queue
// bookkeeping; v1 folds it into rank 9.

// tcpRetxMax is the per-TCB retransmission-queue capacity. Power
// of two lets the index wrap with a bitwise AND.
const tcpRetxMax = 64
const tcpRetxMask = uint8(tcpRetxMax - 1)

// tcpRetxEntry describes one in-flight segment.
type tcpRetxEntry struct {
	seq       uint32 // first seq of the segment
	endSeq    uint32 // seq + payload + SYN/FIN accounting
	flags     uint8  // retransmit with these control flags
	bufOff    uint32 // byte offset into txBuf (0 for SYN/FIN only)
	bufLen    uint16 // payload length (0 for SYN/FIN only)
	sentTicks uint64 // pitTicks at last (re)send
	xmitCount uint8  // 0 = pristine; Karn's rule gates RTT updates
}

// tcpRetxQueue is a bounded ring. Empty iff n==0; full iff
// n==tcpRetxMax. head/tail are uint8 indices masked by tcpRetxMask.
type tcpRetxQueue struct {
	ring [tcpRetxMax]tcpRetxEntry
	head uint8
	tail uint8
	n    uint8
}

// retxPush appends a descriptor at the tail. Caller MUST hold
// tcbTableLock. Returns false if the queue is full.
func retxPush(t *TCB, e tcpRetxEntry) bool {
	q := &t.retxQ
	if q.n >= tcpRetxMax {
		return false
	}
	q.ring[q.tail&tcpRetxMask] = e
	q.tail++
	q.n++
	return true
}

// retxHead returns a pointer to the oldest-unacked descriptor, or
// nil if the queue is empty. Caller MUST hold tcbTableLock.
func retxHead(t *TCB) *tcpRetxEntry {
	q := &t.retxQ
	if q.n == 0 {
		return nil
	}
	return &q.ring[q.head&tcpRetxMask]
}

// retxAckTo pops descriptors whose endSeq <= ack. Returns the
// count popped, the oldest sentTicks among the popped entries (0
// if none popped), and whether at least one pristine (xmitCount
// == 0) entry was popped (signal for Karn's rule — RTT samples
// from retransmitted entries are discarded). Caller MUST hold
// tcbTableLock.
func retxAckTo(t *TCB, ack uint32) (popped int, oldestSent uint64, anyPristine bool) {
	q := &t.retxQ
	for q.n > 0 {
		e := &q.ring[q.head&tcpRetxMask]
		if !seqLE(e.endSeq, ack) {
			break
		}
		if popped == 0 || e.sentTicks < oldestSent {
			oldestSent = e.sentTicks
		}
		if e.xmitCount == 0 {
			anyPristine = true
		}
		q.ring[q.head&tcpRetxMask] = tcpRetxEntry{}
		q.head++
		q.n--
		popped++
	}
	return
}

// retxFlush empties the queue (called on tcbFree-equivalent paths
// so a recycled slot doesn't carry ghost entries). Caller MUST
// hold tcbTableLock.
func retxFlush(t *TCB) {
	t.retxQ = tcpRetxQueue{}
}

// --- RTO constants and scanner ---

const (
	tcpRTOMinTicks uint32 = 100  // 1.0 s — RFC 6298 §2.4 floor
	tcpRTOMaxTicks uint32 = 6000 // 60 s  — RFC 6298 §2.5 ceiling
	tcpRTOInitTicks uint32 = 100 // 1.0 s — initial value before first sample
	tcpMaxRetransmits uint8 = 8  // abandon connection after N retries
	tcpRetxScanTicks uint64 = 5  // RTO scanner polls every 50 ms
)

// tcpRTOScannerRunning gates the single-goroutine RTO scanner.
var tcpRTOScannerRunning bool

// tcpStartRTOScanner spawns the global RTO scanner if it isn't
// already running. Called after the first retxPush that arms a
// deadline. Caller MUST hold tcbTableLock.
func tcpStartRTOScanner() {
	if tcpRTOScannerRunning {
		return
	}
	tcpRTOScannerRunning = true
	go tcpRTOScannerLoop()
}

// tcpRTOScannerLoop is the kernel-wide RTO scanner. Runs forever
// after spawn; inspects every active TCB each tick, fires retx on
// any whose rtoDeadline has passed. Using a single goroutine (not
// one per TCB) keeps goroutine count bounded without a timer wheel.
// A MINOR deviation from net_tcp_timers_and_rtt.md §3.3 (which
// specifies one goroutine per TCB); the scanner approach gives the
// same observable behaviour at 50 ms scan granularity, well inside
// the 1-second RTO floor.
func tcpRTOScannerLoop() {
	for {
		<-afterTicks(tcpRetxScanTicks)
		tcpRTOScanPass()
	}
}

// tcpRTOScanPass inspects every active TCB for an expired RTO
// or TIME_WAIT deadline. Acquires tcbTableLock during the scan;
// releases it before any outbound TX (to respect the rank 9 >
// rank 5 ordering). RTO expiries are fired via tcpRTOFire;
// TIME_WAIT expiries just free the TCB.
func tcpRTOScanPass() {
	var fireRTO [tcbMax]bool
	var fireTW [tcbMax]bool
	var firePersist [tcbMax]bool
	var fireDelack [tcbMax]bool
	now := pitTicks
	flags := tcbTableLock.Acquire()
	for i := 0; i < tcbMax; i++ {
		t := &tcbTable[i]
		if !t.active {
			continue
		}
		if t.rtoDeadline != 0 && now >= t.rtoDeadline {
			fireRTO[i] = true
		}
		if t.state == tcpStateTimeWait &&
			t.timeWaitDeadline != 0 &&
			now >= t.timeWaitDeadline {
			fireTW[i] = true
		}
		if t.persistDeadline != 0 && now >= t.persistDeadline {
			firePersist[i] = true
		}
		if t.delackDeadline != 0 && now >= t.delackDeadline {
			fireDelack[i] = true
		}
	}
	tcbTableLock.Release(flags)

	for i := 0; i < tcbMax; i++ {
		if fireRTO[i] {
			tcpRTOFire(&tcbTable[i])
		}
		if firePersist[i] {
			tcpPersistFire(&tcbTable[i])
		}
		if fireDelack[i] {
			tcpDelackFire(&tcbTable[i])
		}
		if fireTW[i] {
			tcbFree(&tcbTable[i])
		}
	}
}

// tcpRTOFire retransmits the head-of-queue segment for a TCB.
// Caller does NOT hold tcbTableLock — this function acquires it
// as needed and releases before calling tcpSendSegment to respect
// lock ordering (rank 9 > rank 5 via ipv4Send → netBufLock).
func tcpRTOFire(t *TCB) {
	flags := tcbTableLock.Acquire()
	if !t.active || t.rtoDeadline == 0 {
		tcbTableLock.Release(flags)
		return
	}
	if pitTicks < t.rtoDeadline {
		tcbTableLock.Release(flags)
		return
	}
	// Are we past the retransmit-limit?
	if t.xmitCountHead >= tcpMaxRetransmits {
		// Abandon connection.
		t.rtoDeadline = 0
		retxFlush(t)
		tcbTableLock.Release(flags)
		tcbFree(t)
		return
	}
	head := retxHead(t)
	if head == nil {
		// Nothing to retransmit; clear the deadline.
		t.rtoDeadline = 0
		tcbTableLock.Release(flags)
		return
	}
	// Capture what we need for the re-send.
	retxFlags := head.flags
	retxSeq := head.seq
	// For v1, SYN/FIN re-emit needs only the flag set — no
	// payload reconstruction. Data retransmission is deferred
	// until the echo server routes through txBuf.
	_ = head.bufOff
	_ = head.bufLen

	// Record the retransmission.
	head.xmitCount++
	head.sentTicks = pitTicks
	t.xmitCountHead++
	// RFC 5681 §3.1: genuine RTO fire collapses cwnd. Skip the
	// collapse when the scanner was forced by fast retransmit —
	// tcpCCOnDupAck already set cwnd = ssthresh + 3*mss.
	if t.rtoFastRetx {
		t.rtoFastRetx = false
	} else {
		tcpCCOnRTO(t)
	}
	// RFC 6298 §5.5: exponential back-off, clamped to RTO max.
	t.rtoTicks *= 2
	if t.rtoTicks > tcpRTOMaxTicks {
		t.rtoTicks = tcpRTOMaxTicks
	}
	t.rtoDeadline = pitTicks + uint64(t.rtoTicks)
	// For SYN, roll sndNxt back to iss so the retransmit reuses
	// the original seq. (The original push recorded retxSeq.)
	savedSndNxt := t.sndNxt
	if retxFlags&tcpFlagSYN != 0 {
		t.sndNxt = retxSeq
	}
	tcbTableLock.Release(flags)

	// Retransmit.
	if retxFlags&tcpFlagSYN != 0 {
		// Include the MSS option just like the original.
		var mssOpt [4]byte
		tcpBuildMSSOption(mssOpt[:], t.mssLocal)
		if t.state == tcpStateSynReceived {
			tcpSendSegment(t, tcpFlagSYN|tcpFlagACK, mssOpt[:], nil)
		} else {
			tcpSendSegment(t, tcpFlagSYN, mssOpt[:], nil)
		}
	} else if retxFlags&tcpFlagFIN != 0 {
		tcpSendSegment(t, tcpFlagFIN|tcpFlagACK, nil, nil)
	}
	// (Data retransmit stays deferred — see file header.)

	// Restore sndNxt if we adjusted it.
	if retxFlags&tcpFlagSYN != 0 {
		flags = tcbTableLock.Acquire()
		t.sndNxt = savedSndNxt
		tcbTableLock.Release(flags)
	}
}

// tcpArmRTO sets (or re-arms) the RTO deadline after a push or
// an ACK that leaves retxQ non-empty. Caller MUST hold
// tcbTableLock.
func tcpArmRTO(t *TCB) {
	if t.rtoTicks == 0 {
		t.rtoTicks = tcpRTOInitTicks
	}
	t.rtoDeadline = pitTicks + uint64(t.rtoTicks)
	t.xmitCountHead = 0
	tcpStartRTOScanner()
}
