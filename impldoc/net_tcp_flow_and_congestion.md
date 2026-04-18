# Networking Stack — TCP Flow Control and Congestion Control

Detailed design for send/receive window bookkeeping, zero-window
handling, SWS avoidance, and the RFC 5681 slow-start +
congestion-avoidance + fast-retransmit loop.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Depends on: [`net_tcp_state_machine.md`](net_tcp_state_machine.md)
(TCB struct fields sndWnd / rcvWnd / cwnd / ssthresh / dupAcks),
[`net_tcp_timers_and_rtt.md`](net_tcp_timers_and_rtt.md)
(RTO back-off interaction).

---

## 1. Goals

1. Track the peer-advertised receive window (`sndWnd`) and
   respect it: never have more than `sndWnd` bytes outstanding.
2. Advertise our own receive window (`rcvWnd`) honestly —
   bytes of free space in `t.rxBuf`.
3. Implement zero-window persist probing so the connection
   recovers cleanly if the peer's buffer stays full.
4. Avoid Silly Window Syndrome (SWS) on both sides.
5. Implement RFC 5681 slow start, congestion avoidance, fast
   retransmit, and fast recovery. Enough for correct behaviour
   against QEMU slirp's occasional packet drops.

### 1.1 Non-goals

- SACK-enabled recovery (RFC 6675). No SACK in v1.
- TCP timestamps (RFC 7323). No timestamps in v1.
- ECN (RFC 3168).
- Window scale — the 16-bit window field is used as-is,
  capping `rcvWnd` at 65 535 bytes.
- Nagle's algorithm — explicitly off in v1 per
  `net_tcp_overview.md §1.2`.

---

## 2. Receive Window Bookkeeping

### 2.1 Initial advertisement

On SYN_SENT send and SYN_RECEIVED SYN|ACK send,
`rcvWnd = tcpRxBufSize` (8 KiB, per
[`net_tcp_buffers.md §3`](net_tcp_buffers.md)).

### 2.2 Updates

After in-order data is copied into `t.rxBuf` in ESTABLISHED:

```
rcvWnd = tcpRxBufSize - t.rxBuf.len()
```

`rxBuf.len()` is the bytes currently buffered for the user to
read. When the user consumes data via `sys_tcp_recv`, the
buffer drains and `rcvWnd` grows.

### 2.3 Advertising strategy

- On every outbound segment (data, pure ACK, SYN|ACK), write
  `t.rcvWnd` into the `Window` field.
- SWS avoidance (see §5) delays announcing small window
  growth until at least `mssEff` or half the buffer is free.

### 2.4 Receive-side out-of-order handling

v1 drops out-of-order data and relies on the peer's
retransmission (per `net_tcp_overview.md §14 Q2`). The
advertised window reflects only in-order accepted bytes.
This simplifies bookkeeping at a modest throughput cost.

### 2.5 Receive window never collapses

Per RFC 1122 §4.2.2.17, the receive edge (`rcvNxt + rcvWnd`)
must not move left even when `rxBuf` grows more full. The
code clamps:

```go
newEdge := t.rcvNxt + t.rcvWnd
if newEdge < oldEdge { newEdge = oldEdge }
t.rcvWnd = newEdge - t.rcvNxt
```

This matters for the SWS / delayed-advertise paths in §5.

---

## 3. Send Window Bookkeeping

### 3.1 Fields

Recap from `net_tcp_state_machine.md §2`:

```go
sndUna uint32 // oldest unacknowledged sequence number
sndNxt uint32 // next sequence number to send
sndWnd uint32 // peer-advertised receive window
sndWl1 uint32 // seq of last window update
sndWl2 uint32 // ack of last window update
```

### 3.2 Updating on incoming ACK

Per RFC 793 §3.9:

```go
if (sndUna <= segAck && segAck <= sndNxt) &&
   (sndWl1 < segSeq ||
    (sndWl1 == segSeq && sndWl2 <= segAck)) {
    sndWnd = uint32(hdr.Window)
    sndWl1 = segSeq
    sndWl2 = segAck
}
```

The `sndWl1` / `sndWl2` guard prevents stale segments from
shrinking a fresh window.

### 3.3 Maximum-in-flight

```go
flightSize := t.sndNxt - t.sndUna
sendAllowed := minU32(t.cwnd, t.sndWnd)
canSend := int64(sendAllowed) - int64(flightSize)
```

`tcpSendSegment` refuses to send data beyond `canSend`. The
user-side `sys_tcp_send` handler blocks on `t.txWake` when
`canSend <= 0` and the `txBuf` is full.

---

## 4. Zero-Window Persist

### 4.1 Trigger

Set when a segment arrives with `Window == 0` while we still
have data to send:

```go
if t.sndWnd == 0 && t.txBuf.len() > 0 && t.persistDeadline == 0 {
    t.persistDeadline = pitTicks + tcpRTOMinTicks
    go tcpPersistGoroutine(t)
}
```

### 4.2 Probe segment

Send a 1-byte segment at `seq = sndUna` (RFC 793 §3.7). The
peer is RFC-required to respond with an ACK including its
current window, even if the ACK repeats a previous seq:

```go
func tcpPersistFire(t *TCB) {
    // Pull 1 byte from txBuf at offset sndUna - txBufBaseSeq.
    probe := t.txBuf.peek(t.sndUna, 1)
    tcpSendSegment(t, tcpFlagACK|tcpFlagPSH, probe)
    // Exponential back-off, capped.
    t.persistDeadline = pitTicks + clampRTO(
        (t.persistDeadline - pitTicks) * 2)
}
```

Do **not** enqueue the probe in `retxQ` — it is already at
`sndUna` which is by definition the head of `retxQ`. A
fresh retransmit of the head on its own schedule covers this.

### 4.3 Cancel

On any incoming segment with non-zero window:

```go
if hdr.Window > 0 {
    t.sndWnd = uint32(hdr.Window)
    t.persistDeadline = 0 // goroutine exits on next wake
    // Wake txWake to resume normal data flow.
    select { case t.txWake <- struct{}{}: default: }
}
```

---

## 5. SWS (Silly Window Syndrome) Avoidance

### 5.1 Receiver side (RFC 1122 §4.2.3.3)

Do not advertise a window smaller than the lesser of:
- `rxBuf.free() >= rxBuf.cap() / 2`, or
- `rxBuf.free() >= mssEff`.

Until one of these thresholds is met after a shrink, the
receiver advertises its previously-sent window value (clamped
by the "never collapse" rule in §2.5).

```go
func tcpAdvertiseWin(t *TCB) uint16 {
    free := uint32(t.rxBuf.cap() - t.rxBuf.len())
    if free < uint32(t.mssEff) && free < t.rxBuf.cap()/2 {
        // Keep old advertisement (don't offer tiny growth).
        return uint16(t.lastAdvWin)
    }
    if free > 0xFFFF {
        free = 0xFFFF
    }
    t.lastAdvWin = free
    return uint16(free)
}
```

`lastAdvWin uint32` is a new TCB field, initialised to
`tcpRxBufSize` on SYN_SENT / SYN_RECEIVED.

### 5.2 Sender side

With Nagle off, the sender is free to emit segments smaller
than MSS. The SWS avoidance on the sender side is therefore
a no-op in v1. If Nagle is later added, the classic
`USER_TIMEOUT` check goes here.

---

## 6. Congestion Control (RFC 5681)

### 6.1 Fields

Recap from `net_tcp_state_machine.md §2`:

```go
cwnd     uint32 // congestion window (bytes)
ssthresh uint32 // slow-start threshold
dupAcks  uint8  // consecutive duplicate ACKs for fast-retransmit trigger
```

### 6.2 Initialisation

On new connection establishment (ESTABLISHED entry):

```go
t.cwnd = iw(t.mssEff)       // initial window — RFC 5681 §3.1
t.ssthresh = 0xFFFFFFFF      // effectively infinite — first loss event sets it
```

`iw(mss)` = RFC 5681 §3.1:

```go
func iw(mss uint16) uint32 {
    switch {
    case mss > 2190:
        return 2 * uint32(mss)
    case mss > 1095:
        return 3 * uint32(mss)
    default:
        return 4 * uint32(mss)
    }
}
```

With v1's default MSS of 536, `iw = 2144`.

### 6.3 Slow start

While `cwnd < ssthresh`, every newly-acked byte (that is, every
ACK that advances `sndUna` by N bytes) increases `cwnd` by
`min(N, mssEff)`:

```go
if t.cwnd < t.ssthresh {
    delta := uint32(mssEff)
    if newlyAcked < delta {
        delta = newlyAcked
    }
    t.cwnd += delta
}
```

This is exponential growth in practice: each RTT doubles
`cwnd` until it reaches `ssthresh`.

### 6.4 Congestion avoidance

When `cwnd >= ssthresh`, RFC 5681 §3.1 equation (2):

```
cwnd += SMSS * SMSS / cwnd   (per ACK, integer-rounded)
```

In the fixed-point style:

```go
// Per-ACK: accumulates in a `cwndAccum` field, applied when
// full MSS is reached.
t.cwndAccum += uint32(mssEff) * uint32(mssEff) / t.cwnd
if t.cwndAccum >= uint32(mssEff) {
    t.cwnd += uint32(mssEff)
    t.cwndAccum -= uint32(mssEff)
}
```

`cwndAccum uint32` is a new TCB field.

### 6.5 Fast retransmit (RFC 5681 §3.2)

On the **third** duplicate ACK:

```go
if t.dupAcks >= 3 {
    // 1. Set ssthresh.
    flightSize := t.sndNxt - t.sndUna
    t.ssthresh = maxU32(flightSize/2, 2*uint32(t.mssEff))
    // 2. Retransmit the head of retxQ NOW, without waiting for RTO.
    retxReSend(t)
    // 3. Enter fast recovery: cwnd = ssthresh + 3*mssEff.
    t.cwnd = t.ssthresh + 3*uint32(t.mssEff)
    t.dupAcks = 0 // reset; further dup-ACKs inflate cwnd
}
```

During fast recovery each further duplicate ACK increments
`cwnd` by one MSS (§3.2 step 4). The first **new** ACK
(not a duplicate) deflates:

```go
t.cwnd = t.ssthresh
```

### 6.6 Loss on RTO

Per RFC 5681 §3.1 and `net_tcp_timers_and_rtt.md §3.2`:

```go
t.ssthresh = maxU32((t.sndNxt - t.sndUna) / 2, 2*uint32(t.mssEff))
t.cwnd = uint32(t.mssEff) // collapse to 1 MSS on RTO
```

This is more aggressive than fast retransmit's `cwnd =
ssthresh + 3*mss` because RTO indicates a heavier loss event.

### 6.7 Duplicate-ACK counting

`dupAcks` is reset on:
- Any ACK that advances `sndUna`.
- Any segment carrying a non-zero-length payload (even if its
  ACK field is unchanged — RFC 5681 §3.2 step 2).
- Any segment with a window change.

Only strict duplicate ACKs (ack field unchanged, no window
change, no payload) increment it.

---

## 7. Interaction with Retransmission Queue

Every code path that calls `retxAckTo(t, ack)` and observes
a non-zero pop:

1. Compute `newlyAcked = sumOfPoppedLengths`.
2. Update congestion control per §6.3 or §6.4 based on current
   `cwnd` vs `ssthresh`.
3. If Karn's rule allows, call `tcpRTTUpdate` (see
   [`net_tcp_timers_and_rtt.md §2.3`](net_tcp_timers_and_rtt.md)).
4. Reset `rtoDeadline` if `retxQ` is still non-empty.
5. Check whether `t.txWake` should fire (now that `canSend`
   has grown).

---

## 8. Lock Considerations

All of §2-§7 runs under `tcbTableLock` (rank 9). None of these
paths touches `netBufLock` (rank 5) directly; outbound sends
release rank 9 before descending into `ipv4Send` /
`e1000Transmit` (see
[`net_tcp_timers_and_rtt.md §7.2`](net_tcp_timers_and_rtt.md)).

The RX path holds rank 9 for the full duration of segment
dispatch, including the congestion-control updates in §6.
Acceptable because every update is O(1) with no external I/O.

---

## 9. LOC Estimate

| Component | Min | Max | File |
|---|---|---|---|
| rcv window update + SWS avoidance | 50 | 80 | `src/tcp_flow.go` |
| snd window update | 20 | 30 | `src/tcp_flow.go` |
| Persist timer hooks | 30 | 50 | `src/tcp_flow.go` |
| Congestion control (slow start + CA) | 40 | 70 | `src/tcp_cc.go` |
| Fast retransmit + fast recovery | 30 | 50 | `src/tcp_cc.go` |
| `iw()` + initial state | 15 | 25 | `src/tcp_cc.go` |
| **Total** | **185** | **305** | — |

---

## 10. Verification Criteria

1. **Window respect**: peer advertises `Window = 512`; we have
   4 KiB to send — only 512 B leaves in the first burst.
2. **Window update**: peer drains buffer, new ACK carries
   `Window = 2048`; next burst is 2048 B (minus MSS
   fragmentation).
3. **Zero-window persist**: peer stalls with `Window = 0`;
   we emit 1-byte probes at 1 s / 2 s / 4 s / 8 s / ... capped
   at 60 s.
4. **Window reopen cancels persist**: after a probe elicits
   `Window = 2048`, the next probe does NOT fire.
5. **SWS avoidance**: receiver `rcvWnd` drops from 8 KiB to
   100 B — we advertise the old value until `rcvWnd` climbs
   back above `mssEff` (536 B).
6. **Slow start**: connection begins; each RTT doubles `cwnd`
   until `ssthresh` or data runs out. Observe via
   `netDiag` dump.
7. **Congestion avoidance**: after `cwnd >= ssthresh`, growth
   is linear (one MSS per RTT, not per ACK).
8. **Fast retransmit**: peer drops segment N, ACKs N-1 three
   more times — we retransmit segment N before RTO fires.
9. **RTO backoff**: RTO fire collapses `cwnd` to `mssEff`;
   next successful ACK triggers slow start again.
10. **Silly-window-avoidance regression**: run a 1 MB upload
    under QEMU user-mode — pcap shows no runts below 100 B
    except the final segment.

---

## 11. Open Questions

1. **Initial `ssthresh`**: v1 uses `0xFFFFFFFF` (infinite).
   Alternative: 64 KiB (pessimistic start). Recommendation:
   infinite — loss events set a realistic value quickly.
2. **Fast-recovery variant**: v1 implements basic fast
   recovery (RFC 5681 §3.2). NewReno (RFC 6582) handles
   multiple-segment loss better but requires more
   bookkeeping. Recommendation: basic; upgrade to NewReno
   after SACK.
3. **cwnd upper cap**: v1 has no explicit cap — `sndWnd`
   (≤ 64 KiB) is the hard ceiling via the `min(cwnd, sndWnd)`
   formula in §3.3. Recommendation: no cap; add if `iperf3`
   identifies a pathology.
4. **Congestion-control-free mode**: some hobby OSes ship
   without CC. Recommendation: keep CC on — required for
   correctness against QEMU slirp loss.

---

## 12. Relationship to Other Documents

- **`net_tcp_overview.md §5`**: design decision TD7
  (congestion-control scope).
- **`net_tcp_state_machine.md §2`**: TCB fields this document
  manipulates.
- **`net_tcp_segment_io.md §5`**: retxQ data structure the
  congestion-control logic feeds from.
- **`net_tcp_timers_and_rtt.md §3.2`**: RTO-fire path that
  collapses `cwnd` to 1 MSS.
- **`net_tcp_buffers.md §3`**: `rxBuf` capacity that defines
  the initial `rcvWnd` advertisement and drives SWS.
