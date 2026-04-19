# Networking Stack — TCP Timers and RTT Estimation

Detailed design for the five timers and the RTT estimator that
drive TCP's reliability and liveness logic.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Depends on: [`net_tcp_state_machine.md`](net_tcp_state_machine.md)
(TCB struct fields), [`net_tcp_segment_io.md`](net_tcp_segment_io.md)
(retransmission queue).

---

## 1. Goals

1. Implement RFC 6298 retransmission-timeout estimation
   (SRTT / RTTVAR / RTO).
2. Implement five per-TCB timers — RTO, Persist, DelACK,
   TIME_WAIT, Connect — all layered on the existing
   `afterTicks(d uint64) <-chan struct{}`
   (`src/afterticks.go:26-36`).
3. Respect the PIT resolution (100 Hz = 10 ms). Clamp all
   timers to a granularity of 1 tick.
4. Keep timer fire-paths free of allocation and of per-CPU
   lock-order inversions.

### 1.1 Non-goals

- High-resolution timers (µs-class). Not justified at the
  current QEMU slirp loop-back RTTs (sub-ms).
- Jitter-less scheduling. `afterTicks` may overshoot by up to
  one tick; the state machine tolerates this.
- Keep-alive timer. Deferred per `net_tcp_overview.md §1.2`.

---

## 2. RFC 6298 RTT Estimation

### 2.1 State fields (on `TCB`)

Recap from `net_tcp_state_machine.md §2`:

```go
srttTicks   uint32 // smoothed RTT scaled ×8 per RFC 6298 §2.2
rttvarTicks uint32 // RTT variance scaled ×4 per RFC 6298 §2.2
rtoTicks    uint32 // current retransmission timeout, in PIT ticks
```

All three are measured in PIT ticks (10 ms each) but stored as
scaled integers so the algorithm can run without floats.

### 2.2 Initialisation (first RTT sample)

On the **first** valid RTT measurement `R` (in ticks) per
RFC 6298 §2.2:

```
SRTT   = R
RTTVAR = R / 2
RTO    = SRTT + max(G, K * RTTVAR)
```

With `K = 4` and `G = 1 tick`:

```go
// tcpRTTInit seeds the estimator on the first RTT sample.
func tcpRTTInit(t *TCB, rTicks uint32) {
    t.srttTicks = rTicks * 8  // scaled ×8
    t.rttvarTicks = (rTicks / 2) * 4 // scaled ×4
    t.rtoTicks = clampRTO(
        (t.srttTicks / 8) +
        maxU32(1, (t.rttvarTicks/4)*4), // K=4
    )
}
```

### 2.3 Subsequent samples (RFC 6298 §2.3)

```
alpha = 1/8,  beta = 1/4
RTTVAR = (1-beta)*RTTVAR + beta*|SRTT - R|
SRTT   = (1-alpha)*SRTT + alpha*R
RTO    = SRTT + max(G, K*RTTVAR)
```

In fixed-point form (SRTT scaled ×8, RTTVAR scaled ×4):

```go
func tcpRTTUpdate(t *TCB, rTicks uint32) {
    // Unscale.
    srtt := t.srttTicks / 8
    rttvar := t.rttvarTicks / 4

    var delta uint32
    if srtt > rTicks {
        delta = srtt - rTicks
    } else {
        delta = rTicks - srtt
    }
    // RTTVAR = 0.75*RTTVAR + 0.25*|SRTT-R|
    t.rttvarTicks = (3*(t.rttvarTicks/4) + delta) // ×4 kept
    // SRTT = 0.875*SRTT + 0.125*R
    t.srttTicks = (7*(t.srttTicks/8) + rTicks) // ×8 kept

    t.rtoTicks = clampRTO(
        (t.srttTicks / 8) +
        maxU32(1, (t.rttvarTicks/4)*4),
    )
}
```

### 2.4 Clamping

```go
const (
    tcpRTOMinTicks = uint32(100)  // 1.0 s — RFC 6298 §2.4
    tcpRTOMaxTicks = uint32(6000) // 60 s  — RFC 6298 §2.5
)

func clampRTO(v uint32) uint32 {
    if v < tcpRTOMinTicks {
        return tcpRTOMinTicks
    }
    if v > tcpRTOMaxTicks {
        return tcpRTOMaxTicks
    }
    return v
}
```

The 1-second floor is unusual for a hobby OS against QEMU
slirp (where RTT is < 1 ms), but it matches RFC 6298 §2.4 and
avoids thrashing on spurious reorderings.

### 2.5 Karn's algorithm

A retransmitted segment **must not** feed the RTT estimator
(the ACK is ambiguous — we can't tell whether it
acknowledges the first or second transmission).
Implementation: `tcpRetxEntry.xmitCount` is incremented on
retransmit; `retxAckTo` returns a boolean indicating whether
any `xmitCount == 0` entry was popped, and only in that case
does the caller invoke `tcpRTTUpdate`.

### 2.6 Exponential back-off on RTO fire

When the RTO timer fires (§3.2), per RFC 6298 §5.5:

```go
t.rtoTicks = clampRTO(t.rtoTicks * 2)
```

RTO is restored (i.e., SRTT/RTTVAR-driven value is
recomputed) only after a subsequent non-retransmit sample.

---

## 3. Retransmission Timer (RTO)

### 3.1 Scheduling

- **Start**: whenever a segment is queued in `retxQ` and no
  RTO deadline is currently set, `rtoDeadline = pitTicks +
  rtoTicks`. The per-TCB RTO goroutine is (re)spawned on
  `afterTicks(rtoTicks)` (see §7 for the goroutine pattern).
- **Restart**: on a valid ACK that advances `sndUna`, if
  `retxQ` is still non-empty, reset
  `rtoDeadline = pitTicks + rtoTicks` (RFC 6298 §5.3).
- **Cancel**: on ACK that empties `retxQ`, `rtoDeadline = 0`
  (goroutine observes this and exits).

### 3.2 Fire action

1. Acquire `tcbTableLock`.
2. If `rtoDeadline == 0` or `pitTicks < rtoDeadline`, spurious
   wake — goto 5.
3. Call `retxReSend(t)` on the head of `retxQ`. This bumps
   `xmitCount` and `sentTicks` inside the descriptor.
4. `t.rtoTicks = clampRTO(t.rtoTicks * 2)` (back-off).
5. Set `t.ssthresh = max(FlightSize/2, 2*mssEff)` and
   `t.cwnd = mssEff` per
   [`net_tcp_flow_and_congestion.md §6`](net_tcp_flow_and_congestion.md).
6. If `xmitCount > tcpMaxRetransmits` (proposed: 8 → total
   elapsed ~2 minutes), abandon the connection: send RST,
   `tcbFree`, wake userspace waiters with an error indication.
7. Otherwise, `rtoDeadline = pitTicks + rtoTicks`; release the
   lock; reschedule a fresh `afterTicks` goroutine.

### 3.3 Timer goroutine

```go
func tcpRTOGoroutine(tcbIdx int) {
    for {
        flags := tcbTableLock.Acquire()
        t := &tcbTable[tcbIdx]
        if !t.active || t.rtoDeadline == 0 {
            tcbTableLock.Release(flags)
            return
        }
        delay := uint64(0)
        if t.rtoDeadline > pitTicks {
            delay = t.rtoDeadline - pitTicks
        }
        tcbTableLock.Release(flags)

        <-afterTicks(delay)

        // Re-enter under lock to check the current deadline.
        flags = tcbTableLock.Acquire()
        if !t.active || t.rtoDeadline == 0 {
            tcbTableLock.Release(flags)
            return
        }
        if pitTicks < t.rtoDeadline {
            // Deadline was pushed out while we slept — loop.
            tcbTableLock.Release(flags)
            continue
        }
        // Fire.
        tcpRTOFire(t) // §3.2 steps 3-7, still under lock
        tcbTableLock.Release(flags)
    }
}
```

One goroutine per active TCB, spawned on first retxQ-push and
exiting when `rtoDeadline == 0`. Using a goroutine per TCB
keeps timer logic linear and avoids a central timer wheel.
With the 16-TCB cap, at most 16 such goroutines ever exist.

---

## 4. Delayed-ACK Timer

Delayed ACK (RFC 1122 §4.2.3.2): piggyback ACKs on outbound
data. If no outbound data is ready, fire after a short delay.

### 4.1 Parameters

- `tcpDelACKTicks = 20` (200 ms) — matches Linux default and
  is well above the PIT floor.

### 4.2 Scheduling

- On receipt of in-order data (in ESTABLISHED, FIN_WAIT_1,
  FIN_WAIT_2) with no outbound data queued for that TCB,
  set `delackDeadline = pitTicks + tcpDelACKTicks`.
- If `delackDeadline` is already set, do **not** reset —
  multiple unacknowledged segments accelerate the pure-ACK
  (RFC 1122: "send an ACK for at least every second
  full-sized segment" — implemented by immediately sending
  ACK when `rcvNxt - lastAckSent >= 2 * mssEff`).
- On any outbound data send or pure ACK send,
  `delackDeadline = 0`.

### 4.3 Fire action

1. Acquire `tcbTableLock`.
2. If `delackDeadline == 0` or `pitTicks < delackDeadline`,
   spurious wake — return.
3. Call `tcpSendSegment(t, tcpFlagACK, nil)`.
4. `delackDeadline = 0`.

### 4.4 Interaction with Nagle

Nagle is off in v1 (per `net_tcp_overview.md §1.2`). Delayed
ACK is safe with Nagle off; the classic 200 ms stall arises
only when both are on at opposite ends of the connection.

---

## 5. TIME_WAIT Timer

Fires when the TCB has been in TIME_WAIT for `tcpTimeWaitTicks`.

### 5.1 Parameters

- `tcpTimeWaitTicks = 6000` (60 s) — matches TD5 in
  `net_tcp_overview.md §5`.

### 5.2 Scheduling

- On transition into TIME_WAIT (via any of FIN_WAIT_1,
  FIN_WAIT_2, CLOSING),
  `timeWaitDeadline = pitTicks + tcpTimeWaitTicks`.
- If a retransmitted FIN arrives, re-ACK and
  `timeWaitDeadline = pitTicks + tcpTimeWaitTicks` (reset the
  timer — RFC 793 §3.5).

### 5.3 Fire action

1. Acquire `tcbTableLock`.
2. `tcbFree(t)`.
3. The slot becomes available for a new passive or active
   open.

---

## 6. Persist and Connect Timers

### 6.1 Persist timer

Fires when the peer has advertised zero window (`sndWnd == 0`)
and we have data to send. Probes the peer periodically until
the window opens.

- Start: on receipt of a window update reducing `sndWnd` to 0
  while `len(txBuf) > 0`.
- Schedule: initial delay = `tcpRTOMinTicks` (1 s); exponential
  back-off up to `tcpRTOMaxTicks` (60 s).
- Fire: send a 1-byte "window probe" segment with
  `seq = sndUna` and flags `ACK`. The peer's response updates
  `sndWnd`.
- Cancel: on any window update raising `sndWnd > 0`.

Full discussion in
[`net_tcp_flow_and_congestion.md §4`](net_tcp_flow_and_congestion.md).

### 6.2 Connect timer

Used only in SYN_SENT. Fires if the SYN is not ACKed within
the retry schedule (TD-open in `net_tcp_overview.md §14 Q4`):
1 s, 3 s, 7 s.

- Start: on transition into SYN_SENT,
  `connectDeadline = pitTicks + 100` (1 s).
- Fire action: retransmit SYN; set
  `connectDeadline = pitTicks + nextDelay`; if we have already
  retransmitted three times, abandon:
  `tcpError(t, fdErrBad)`, `tcbFree`.
- Cancel: on transition into ESTABLISHED.

---

## 7. Timer-Goroutine Pattern

Every timer uses the same pattern as §3.3:

1. A dedicated goroutine per `(TCB, timer-kind)` pair.
2. The goroutine blocks on `afterTicks(delay)`.
3. On wake, re-acquires `tcbTableLock`, checks that the
   deadline is still the one it was scheduled for (not
   cancelled or reset), fires the handler, reschedules or
   exits.
4. On TCB free, all deadline fields are zeroed; goroutines
   observe this on their next wake and exit.

This pattern has three desirable properties:
- **No timer wheel**: simpler to reason about; bounded by the
  16-TCB × 5-timer = 80 max goroutines across the whole
  kernel.
- **Cancellable**: cancellation is a lock-protected field
  write — no goroutine kill-signal needed.
- **`afterTicks`-native**: re-uses a primitive already
  verified in the existing net stack (`src/arp.go:220-240`
  uses it for ARP resolve timeouts).

### 7.1 Avoiding goroutine leaks

- On `tcbFree`, every deadline field is set to zero **before**
  the lock is released. The next wake of any running timer
  goroutine sees the zero and exits cleanly.
- If a timer is cancelled and re-started (e.g., RTO restart on
  fresh ACK), the existing goroutine observes the new later
  deadline on its next wake, sleeps again, and fires at the
  new time. No new goroutine is spawned on restart.
- If a timer is started anew after a full cancel-and-exit
  cycle, a fresh goroutine is spawned. The code path must
  detect the "goroutine dead" state — easiest: a
  `rtoGoroutineRunning bool` field per TCB, set true on
  spawn, cleared on exit.

### 7.2 ISR-safety

- The goroutine itself runs from the TinyGo scheduler, not
  from ISR. Lock-acquire safe.
- The fire handler acquires rank 9 (`tcbTableLock`) to mutate
  TCB state, and may additionally touch rank 11
  (`tcpTimerLock`) for per-timer bookkeeping — always rank 9
  first, rank 11 second.
- The fire handler **must** release `tcbTableLock` (and
  `tcpTimerLock` if held) before invoking `ipv4Send` /
  `e1000Transmit`, because those inner functions eventually
  touch `netBufLock` (rank 5). Acquiring rank 5 while rank 9
  is held would invert the ordering
  (`src/spinlock.go`'s "hold N, never acquire M where M < N"
  invariant). The code structure is:

```go
flags := tcbTableLock.Acquire()
// ... mutate TCB state, build segment bytes ...
seg := buildSegmentLocal(t) // on stack (no alloc)
tcbTableLock.Release(flags)

// Now safe to descend into the lower-rank side of the stack.
// ipv4Send is the 3-arg form at src/ipv4.go:154 — source IP
// is implicit ourIP. TCB's t.localIP equals ourIP in v1; it
// is identity bookkeeping, not passed to ipv4Send.
ipv4Send(ipProtoTCP, t.remoteIP, seg)
// ipv4Send takes netBufLock internally; rank 5 is acquired
// with no TCP lock held.
```

Precedent: `src/udp.go:237-262` (`udpSend`) enters holding
no `udpLock` and descends straight into `ipv4Send`. The
`udpLock` (rank 7) is only taken inside bind-table operations
(`udpBind`, `udpBindWithChannel`, `udpUnbind`), never while
TX-ing. TCP follows the same split: TCB-table mutations are
strictly separated from the send path.

---

## 8. LOC Estimate

| Component | Min | Max | File |
|---|---|---|---|
| RFC 6298 SRTT/RTTVAR/RTO calc | 40 | 60 | `src/tcp_rtt.go` |
| RTO timer goroutine + scheduling | 60 | 100 | `src/tcp_retx.go` |
| DelACK timer | 30 | 50 | `src/tcp_retx.go` |
| TIME_WAIT timer | 25 | 45 | `src/tcp_retx.go` |
| Persist timer | 40 | 70 | `src/tcp_retx.go` |
| Connect timer | 25 | 45 | `src/tcp_retx.go` |
| Timer-goroutine-running bookkeeping | 20 | 30 | `src/tcp_retx.go` |
| **Total** | **240** | **400** | — |

---

## 9. Verification Criteria

1. **RTT-sample seed**: a first sample of 5 ticks yields
   `srttTicks = 40`, `rttvarTicks = 10`, `rtoTicks` clamped to
   100.
2. **Second sample**: following seed, sample 3 ticks yields
   `rttvar` decreased, `srtt` decreased, `rto` still clamped
   to 100.
3. **RTO back-off**: simulate three RTO fires in a row — each
   doubles `rtoTicks`, capped at 6000.
4. **Karn's rule**: retransmit a segment, then the peer ACKs
   it. Verify `tcpRTTUpdate` is NOT called.
5. **Delayed ACK**: send 500 B of data; no outbound data on
   the other direction; verify an ACK is sent 200 ms later.
6. **Delayed ACK accelerated**: receive two full segments;
   verify an ACK is sent immediately (not after 200 ms).
7. **TIME_WAIT**: after FIN exchange, TCB stays in TIME_WAIT
   for 60 s, then is freed.
8. **Connect timeout**: disable host response; `connect()`
   retries at 1 s, 3 s, 7 s, then errors out.
9. **Persist timer**: peer advertises zero window; probe
   segments fire at 1 s, 2 s, 4 s, ... capped at 60 s; cancel
   on first non-zero window update.
10. **No goroutine leak**: spawn and tear down 16 TCBs in a
    loop; goroutine count returns to baseline
    (monitor via `serialPrint` of TinyGo `runtime.NumTask()`).

---

## 10. Open Questions

1. **RTO floor**: RFC 6298 §2.4 says "SHOULD be rounded up to
   1 s". QEMU slirp RTT is < 1 ms, so a 1 s floor is
   conservative. Recommendation: accept the floor; revisit
   only if v2 adds SACK and we observe throughput stalls.
2. **`tcpMaxRetransmits = 8`**: with the back-off, this is
   approximately `1+2+4+8+16+32+60+60 = 183 seconds` before
   giving up. Recommendation: accept; matches common Linux
   `net.ipv4.tcp_retries2 = 15` in spirit but shorter.
3. **Delayed-ACK threshold**: "every second full-sized
   segment" is commonly phrased but §4 implements it as
   `rcvNxt - lastAckSent >= 2 * mssEff`. Recommendation:
   accept; revisit if host-side Linux observed to
   pause because we're not ACK-ing often enough.

---

## 11. Relationship to Other Documents

- **`net_tcp_overview.md §5`**: design decisions TD5
  (TIME_WAIT duration) and §14 Q4 (connect schedule).
- **`net_tcp_state_machine.md §2`**: TCB fields
  (`rtoDeadline` / `delackDeadline` / `timeWaitDeadline` /
  `persistDeadline` / `connectDeadline`).
- **`net_tcp_segment_io.md §5`**: the retransmission queue
  these timers drive.
- **`net_tcp_flow_and_congestion.md §6`**: cwnd/ssthresh
  updates on RTO fire.
- **`src/afterticks.go:26-36`**: the primitive every timer
  uses.
- **`src/arp.go:220-240`**: precedent for `afterTicks`-based
  timeouts in the gooos networking stack.
