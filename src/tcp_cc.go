package main

// TCP congestion control — RFC 5681 slow start + congestion
// avoidance, fast retransmit, fast recovery, RTO-triggered
// collapse. Design: impldoc/net_tcp_flow_and_congestion.md §6.
//
// All helpers read/write TCB fields guarded by tcbTableLock
// (rank 9). Callers must hold the lock; helpers never take it.
//
// Scope note: for v1 the kernel echo server doesn't stage TX
// data in t.txBuf, so data-driven slow-start ramp-up isn't
// exercised by the current smoke test. The bookkeeping is in
// place so that once sys_tcp_send (TCP-5) routes through
// txBuf, throughput immediately picks up proper CC.

// tcpInitialWindow returns the RFC 5681 §3.1 initial cwnd in
// bytes for a given MSS. With v1's tcpDefaultMSS = 536, iw() =
// 4 * 536 = 2144 B.
func tcpInitialWindow(mss uint16) uint32 {
	m := uint32(mss)
	switch {
	case m > 2190:
		return 2 * m
	case m > 1095:
		return 3 * m
	default:
		return 4 * m
	}
}

// tcpCCInit seeds cwnd / ssthresh on transition into ESTABLISHED.
// ssthresh starts "arbitrarily high" (max uint32) so slow start
// runs until the first loss event. Caller MUST hold
// tcbTableLock.
func tcpCCInit(t *TCB) {
	if t.mssEff == 0 {
		t.mssEff = tcpDefaultMSS
	}
	t.cwnd = tcpInitialWindow(t.mssEff)
	t.ssthresh = 0xFFFFFFFF
	t.cwndAccum = 0
	t.dupAcks = 0
}

// tcpCCOnAck updates cwnd for a newly-acknowledged byte count
// per RFC 5681. newlyAcked is the number of bytes the peer just
// ACKed (derived by the caller from sndUna advance). Caller
// MUST hold tcbTableLock.
//
// Slow start: cwnd += min(newlyAcked, mssEff) per ACK.
// Congestion avoidance: cwnd += mssEff*mssEff/cwnd per ACK (using
// cwndAccum to keep track of the fractional-byte residual until
// it crosses mssEff and can be applied).
func tcpCCOnAck(t *TCB, newlyAcked uint32) {
	if newlyAcked == 0 {
		return
	}
	if t.cwnd == 0 {
		// Shouldn't happen post-tcpCCInit, but guard anyway.
		tcpCCInit(t)
	}
	mss := uint32(t.mssEff)
	if mss == 0 {
		mss = uint32(tcpDefaultMSS)
	}
	if t.cwnd < t.ssthresh {
		// Slow start.
		inc := newlyAcked
		if inc > mss {
			inc = mss
		}
		t.cwnd += inc
		return
	}
	// Congestion avoidance.
	t.cwndAccum += (mss * mss) / t.cwnd
	if t.cwndAccum >= mss {
		t.cwnd += mss
		t.cwndAccum -= mss
	}
}

// tcpCCOnDupAck handles a duplicate ACK. Increments dupAcks;
// on the 3rd consecutive dup, transitions into fast retransmit /
// fast recovery: ssthresh = max(flight/2, 2*mss), retransmit
// head, cwnd = ssthresh + 3*mss.
// Returns true iff fast retransmit fires — caller is expected
// to invoke retxReSend outside the lock afterwards.
// Caller MUST hold tcbTableLock.
func tcpCCOnDupAck(t *TCB) bool {
	t.dupAcks++
	if t.dupAcks != 3 {
		return false
	}
	mss := uint32(t.mssEff)
	if mss == 0 {
		mss = uint32(tcpDefaultMSS)
	}
	flight := t.sndNxt - t.sndUna
	half := flight / 2
	min2mss := 2 * mss
	if half < min2mss {
		t.ssthresh = min2mss
	} else {
		t.ssthresh = half
	}
	t.cwnd = t.ssthresh + 3*mss
	t.cwndAccum = 0
	// Caller will handle the actual retransmit.
	return true
}

// tcpCCOnRTO applies the RFC 5681 §3.1 collapse when RTO fires:
// ssthresh = max(flight/2, 2*mss), cwnd = mss. Caller MUST hold
// tcbTableLock.
func tcpCCOnRTO(t *TCB) {
	mss := uint32(t.mssEff)
	if mss == 0 {
		mss = uint32(tcpDefaultMSS)
	}
	flight := t.sndNxt - t.sndUna
	half := flight / 2
	min2mss := 2 * mss
	if half < min2mss {
		t.ssthresh = min2mss
	} else {
		t.ssthresh = half
	}
	t.cwnd = mss
	t.cwndAccum = 0
	t.dupAcks = 0
}

// tcpIsDuplicateACK tells whether a segment looks like a pure
// dup-ACK worth counting toward fast retransmit. Per RFC 5681
// §3.2: ACK set, Ack == sndUna (no advance), no payload, no
// SYN/FIN/RST, no window change.
// Caller MUST hold tcbTableLock.
func tcpIsDuplicateACK(t *TCB, h TCPHeader, payloadLen int) bool {
	if h.Flags&tcpFlagACK == 0 {
		return false
	}
	if h.Flags&(tcpFlagSYN|tcpFlagFIN|tcpFlagRST) != 0 {
		return false
	}
	if payloadLen != 0 {
		return false
	}
	if h.Ack != t.sndUna {
		return false
	}
	if uint32(h.Window) != t.sndWnd {
		return false
	}
	// sndUna must lag sndNxt, otherwise there's nothing in flight
	// that could be dup-ACKed.
	if t.sndUna == t.sndNxt {
		return false
	}
	return true
}
