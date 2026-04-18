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
	}
	return h.Ack == t.sndNxt
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
