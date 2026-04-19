package main

// TCP RTT estimation per RFC 6298.
//
// The estimator keeps a smoothed RTT (SRTT) and RTT variance
// (RTTVAR) in fixed-point form so the math runs without floats:
//   - srttTicks is SRTT scaled ×8 (alpha = 1/8 shifts cleanly).
//   - rttvarTicks is RTTVAR scaled ×4 (beta = 1/4 shifts cleanly).
// Constants K = 4, G = 1 tick (100 Hz PIT = 10 ms).
//
// Caller contract: every call runs under tcbTableLock (the TCB's
// srtt/rttvar/rto fields are all rank-9 state). rtoTicks is
// clamped to [tcpRTOMinTicks, tcpRTOMaxTicks] on update so the
// RTO scanner always reads a sane value.
//
// Karn's algorithm is enforced outside this file: the retx-ack
// path only invokes the sampler when at least one pristine
// descriptor (xmitCount == 0) was popped.

// tcpRTTInit seeds the estimator on the first RTT sample R (in
// PIT ticks):
//   SRTT   = R
//   RTTVAR = R/2
//   RTO    = SRTT + max(G, K*RTTVAR)
// Caller MUST hold tcbTableLock.
func tcpRTTInit(t *TCB, rTicks uint32) {
	if rTicks == 0 {
		rTicks = 1 // clamp to at least one PIT tick
	}
	t.srttTicks = rTicks * 8   // scaled ×8
	t.rttvarTicks = (rTicks / 2) * 4 // scaled ×4 (zero-safe)
	if t.rttvarTicks == 0 {
		// RTTVAR at least G when R/2 rounds to zero.
		t.rttvarTicks = 1 * 4
	}
	t.rtoTicks = clampRTO(rttvarBasedRTO(t))
	t.rttInitialized = true
}

// tcpRTTUpdate applies a fresh sample R to an existing estimator:
//   RTTVAR = (1-beta)*RTTVAR + beta*|SRTT-R|
//   SRTT   = (1-alpha)*SRTT + alpha*R
//   RTO    = SRTT + max(G, K*RTTVAR)
// With alpha = 1/8, beta = 1/4. Scaled forms:
//   rttvarTicks = 3*(rttvarTicks/4) + |SRTT-R|   (×4 preserved)
//   srttTicks   = 7*(srttTicks/8)   + R          (×8 preserved)
// Caller MUST hold tcbTableLock.
func tcpRTTUpdate(t *TCB, rTicks uint32) {
	if !t.rttInitialized {
		tcpRTTInit(t, rTicks)
		return
	}
	if rTicks == 0 {
		rTicks = 1
	}
	// Current SRTT in un-scaled ticks.
	srtt := t.srttTicks / 8
	var delta uint32
	if srtt > rTicks {
		delta = srtt - rTicks
	} else {
		delta = rTicks - srtt
	}
	t.rttvarTicks = 3*(t.rttvarTicks/4) + delta
	t.srttTicks = 7*(t.srttTicks/8) + rTicks
	t.rtoTicks = clampRTO(rttvarBasedRTO(t))
}

// tcpRTTSample is the glue between the retx-ack path and the
// estimator. Takes the oldest sentTicks of the popped entries
// and whether any pristine entries were popped (Karn's rule).
// If anyPristine == false the sample is discarded.
// Caller MUST hold tcbTableLock.
func tcpRTTSample(t *TCB, oldestSent uint64, anyPristine bool) {
	if !anyPristine {
		return
	}
	if pitTicks <= oldestSent {
		return // clock skew or same-tick round-trip; skip
	}
	r := uint32(pitTicks - oldestSent)
	tcpRTTUpdate(t, r)
}

// rttvarBasedRTO computes SRTT + max(G, K*RTTVAR) from the TCB's
// scaled fields. G = 1, K = 4.
func rttvarBasedRTO(t *TCB) uint32 {
	srtt := t.srttTicks / 8
	// K*RTTVAR with RTTVAR already scaled ×4 becomes rttvarTicks
	// as-is (K=4 cancels the ×4 scaling).
	kRttvar := t.rttvarTicks
	if kRttvar < 1 {
		kRttvar = 1 // G floor
	}
	return srtt + kRttvar
}

// clampRTO enforces RFC 6298 §2.4 / §2.5 bounds.
func clampRTO(v uint32) uint32 {
	if v < tcpRTOMinTicks {
		return tcpRTOMinTicks
	}
	if v > tcpRTOMaxTicks {
		return tcpRTOMaxTicks
	}
	return v
}
