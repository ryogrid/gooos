package main

// TCP flow control — receive-window advertisement with Silly
// Window Syndrome avoidance per RFC 1122 §4.2.3.3, send-window
// tracking glue, and scaffolding for the persist and
// delayed-ACK timers. Design: net_tcp_flow_and_congestion.md.
//
// Caller contract: these helpers read/write TCB fields that are
// protected by tcbTableLock (rank 9). In v1 BSP-only gooos the
// locking discipline is relaxed for the advertise-win path
// (tcpSendSegment reads rcvWnd/lastAdvWin without the lock for
// lock-order reasons — see comment in tcpSendSegment).

// Persist-timer constants.
const (
	tcpPersistInitTicks uint32 = 100  // 1 s — RTO floor
	tcpPersistMaxTicks  uint32 = 6000 // 60 s — RTO ceiling
)

// tcpMaybeArmPersist arms the persist timer if the peer has
// advertised a zero send window and we have data pending in
// txBuf. A no-op otherwise. Caller MUST hold tcbTableLock.
func tcpMaybeArmPersist(t *TCB) {
	if t.sndWnd != 0 {
		t.persistDeadline = 0
		t.persistTicks = 0
		return
	}
	if t.txBuf.rbLen() == 0 {
		return
	}
	if t.persistDeadline == 0 {
		t.persistTicks = tcpPersistInitTicks
		t.persistDeadline = pitTicks + uint64(t.persistTicks)
		tcpStartRTOScanner()
	}
}

// tcpPersistFire fires a 1-byte window probe for a TCB whose
// persist timer expired. Scanner wakes us; we back off the
// interval (doubling to the RTO ceiling) and send a 1-byte
// segment drawn from txBuf at sndUna. If txBuf is empty (peer
// raced us to ACK their own window update), just clear the
// timer. Caller does NOT hold tcbTableLock.
//
// Note: the probe send path needs the echo-server refactor to
// actually carry meaningful data. Current kernel-side
// consumers don't stage bytes in txBuf, so the probe would be
// zero-length. Guarded against that below.
func tcpPersistFire(t *TCB) {
	flags := tcbTableLock.Acquire()
	if !t.active || t.persistDeadline == 0 {
		tcbTableLock.Release(flags)
		return
	}
	if pitTicks < t.persistDeadline {
		tcbTableLock.Release(flags)
		return
	}
	have := t.txBuf.rbLen()
	if have == 0 || t.sndWnd != 0 {
		t.persistDeadline = 0
		t.persistTicks = 0
		tcbTableLock.Release(flags)
		return
	}
	// Back off and re-arm.
	if t.persistTicks < tcpPersistMaxTicks {
		t.persistTicks *= 2
		if t.persistTicks > tcpPersistMaxTicks {
			t.persistTicks = tcpPersistMaxTicks
		}
	}
	t.persistDeadline = pitTicks + uint64(t.persistTicks)
	// Copy a single byte at sndUna (= txBuf head) for the probe.
	var probe [1]byte
	t.txBuf.rbPeek(0, 1, probe[:])
	tcbTableLock.Release(flags)

	// Send as ACK|PSH with 1 byte payload — peer ACKs even a
	// zero-window probe, which gets us a fresh window value.
	tcpSendSegment(t, tcpFlagACK|tcpFlagPSH, nil, probe[:])
}

// tcpAckUpdate processes the ACK field of an incoming segment
// and applies it to the TCB. Handles three things in RFC-
// canonical order:
//   1. sndUna advance (if the ACK is in (sndUna, sndNxt]),
//      with retxAckTo pop + RTT sample (Karn's rule) and
//      RTO re-arm.
//   2. RFC 793 §3.9 send-window update using the
//      sndWl1/sndWl2 staleness guard.
// Returns true if this ACK acknowledges up-to-and-including our
// sndNxt — a signal callers use to detect "ACK of our FIN" when
// a FIN was queued.
// Caller MUST hold tcbTableLock.
func tcpAckUpdate(t *TCB, h TCPHeader) bool {
	if h.Flags&tcpFlagACK == 0 {
		return false
	}
	if !seqLE(t.sndUna, h.Ack) || !seqLE(h.Ack, t.sndNxt) {
		return false
	}
	if t.sndUna != h.Ack {
		t.sndUna = h.Ack
		_, oldestSent, anyPristine := retxAckTo(t, h.Ack)
		tcpRTTSample(t, oldestSent, anyPristine)
		if t.retxQ.n == 0 {
			t.rtoDeadline = 0
		} else {
			tcpArmRTO(t)
		}
	}
	// RFC 793 §3.9 send-window update. The guard keeps a stale
	// segment (old seq or old ack) from shrinking a freshly-
	// advertised window.
	if seqLT(t.sndWl1, h.Seq) ||
		(t.sndWl1 == h.Seq && seqLE(t.sndWl2, h.Ack)) {
		t.sndWnd = uint32(h.Window)
		t.sndWl1 = h.Seq
		t.sndWl2 = h.Ack
		tcpMaybeArmPersist(t) // new window might be zero
	}
	return h.Ack == t.sndNxt
}

// Delayed-ACK constants.
const tcpDelackTicks uint64 = 20 // 200 ms at 100 Hz PIT

// tcpDelackFire emits a pure ACK when the delayed-ACK deadline
// expires. Clears the deadline afterward. Caller does NOT hold
// tcbTableLock.
func tcpDelackFire(t *TCB) {
	flags := tcbTableLock.Acquire()
	if !t.active || t.delackDeadline == 0 {
		tcbTableLock.Release(flags)
		return
	}
	if pitTicks < t.delackDeadline {
		tcbTableLock.Release(flags)
		return
	}
	t.delackDeadline = 0
	tcbTableLock.Release(flags)
	tcpSendPureACK(t)
}

// tcpAdvertiseWin computes the receive window to advertise on
// the next outbound segment. Applies SWS avoidance: does not
// announce growth smaller than min(mssEff, cap/2). Updates
// t.lastAdvWin when the new window is announced so future
// comparisons use the most-recently-advertised value as the
// baseline.
//
// NOT lock-protected — see caller contract above. Safe under
// BSP-only scheduling where the state machine and
// tcpSendSegment cannot run concurrently on the same TCB.
func tcpAdvertiseWin(t *TCB) uint16 {
	free := t.rcvWnd
	last := t.lastAdvWin
	capBytes := uint32(tcpRxBufSize)
	threshold := capBytes / 2
	mss := uint32(t.mssEff)
	if mss != 0 && mss < threshold {
		threshold = mss
	}
	// If we're growing the window, only announce growth that
	// exceeds the SWS threshold. Shrinking is allowed freely
	// (RFC 1122 §4.2.2.17's "never move the right edge left"
	// is enforced by the rcvWnd bookkeeping, not here).
	if free > last {
		if free-last < threshold {
			if last > 0xFFFF {
				return 0xFFFF
			}
			return uint16(last)
		}
	}
	if free > 0xFFFF {
		t.lastAdvWin = 0xFFFF
		return 0xFFFF
	}
	t.lastAdvWin = free
	return uint16(free)
}
