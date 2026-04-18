# Networking Stack — TCP State Machine and TCB

Detailed design for the RFC 793 TCP connection state machine,
the Transmission Control Block (TCB) struct, the listen-port
table, the accept queue, and ISN generation.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Depends on: [`net_tcp_segment_io.md`](net_tcp_segment_io.md)
(header parse/build and checksum).

---

## 1. Goals

1. Implement all eleven RFC 793 states: `CLOSED`, `LISTEN`,
   `SYN_SENT`, `SYN_RECEIVED`, `ESTABLISHED`, `FIN_WAIT_1`,
   `FIN_WAIT_2`, `CLOSING`, `CLOSE_WAIT`, `LAST_ACK`, `TIME_WAIT`.
2. Drive the machine from three event sources:
   - **Syscall events** (user-initiated) — `listen`, `accept`,
     `connect`, `send`, `recv`, `close`, `shutdown`.
   - **Segment events** (peer-initiated) — arrival of any TCP
     segment delivered by `tcpHandle`.
   - **Timer events** — RTO expiry, persist-timer expiry,
     delayed-ACK expiry, TIME_WAIT expiry, connect-timeout.
3. Keep all TCB mutations protected by `tcbTableLock`
   (`net_tcp_overview.md §10`, rank 9) so the ISR-side
   `tcpHandle` and the Ring-3 syscall handlers cannot race.
4. Never allocate from inside `tcpHandle`; every TCB slot is
   pre-allocated in a fixed-size table.

### 1.1 Non-goals

- Half-close receive side (`SHUT_RD`).
- Simultaneous open race (two peers SYN-ing at once) — v1 rejects
  with RST.
- TCP option state carried across connections (timestamps /
  window scale) — no options other than MSS are negotiated.

---

## 2. TCB Struct

```go
// tcbState is one of tcpStateClosed..tcpStateTimeWait.
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

// TCB — Transmission Control Block.
// Protected by tcbTableLock (rank 9). One TCB per active
// connection. See net_tcp_overview.md §10 for lock ordering.
type TCB struct {
    // 4-tuple identity (host byte order throughout the kernel).
    localIP    uint32
    localPort  uint16
    remoteIP   uint32
    remotePort uint16

    // State.
    state    tcbState
    listener *tcpListener // non-nil if this TCB came from a LISTEN

    // Send sequence space (RFC 793 §3.2).
    sndUna uint32 // oldest unacknowledged sequence number
    sndNxt uint32 // next sequence number to send
    sndWnd uint32 // peer-advertised receive window (bytes)
    sndWl1 uint32 // seq of last window update
    sndWl2 uint32 // ack of last window update
    iss    uint32 // initial send sequence number

    // Receive sequence space (RFC 793 §3.2).
    rcvNxt uint32 // next expected sequence number
    rcvWnd uint32 // our advertised receive window (bytes)
    irs    uint32 // initial receive sequence number

    // MSS negotiation.
    mssLocal uint16 // what we advertised (default 536)
    mssPeer  uint16 // what the peer advertised (default 536)
    mssEff   uint16 // min(mssLocal, mssPeer); used when sending

    // Send/receive ring buffers. See net_tcp_buffers.md §3.
    txBuf    tcpRingBuf // cap 8 KiB
    rxBuf    tcpRingBuf // cap 8 KiB

    // Retransmission queue. See net_tcp_segment_io.md §5.
    retxQ    tcpRetxQueue

    // RTT estimator. See net_tcp_timers_and_rtt.md §2.
    srttTicks  uint32 // scaled ×8 per RFC 6298 §2
    rttvarTicks uint32 // scaled ×4 per RFC 6298 §2
    rtoTicks   uint32 // clamped to [100, 6000] (1 s .. 60 s)

    // Congestion control. See net_tcp_flow_and_congestion.md §6.
    cwnd     uint32
    ssthresh uint32
    dupAcks  uint8

    // Timers. Each field holds the deadline (in pitTicks) when
    // the timer fires; zero = inactive. See net_tcp_timers_and_rtt.md.
    rtoDeadline     uint64
    persistDeadline uint64
    delackDeadline  uint64
    timeWaitDeadline uint64
    connectDeadline uint64

    // Blocking-recv wakeup. One goroutine per TCB waits on this
    // channel in sys_tcp_recv when rxBuf is empty. Capacity 1.
    rxWake chan struct{}

    // Blocking-send wakeup. One goroutine per TCB waits on this
    // channel in sys_tcp_send when txBuf is full. Capacity 1.
    txWake chan struct{}

    // Bookkeeping.
    userOwner int  // owning pid, -1 if kernel-internal
    active    bool // false = slot is free
}

// tcbTable is the single kernel-wide TCB pool.
// Fixed cap keeps memory bounded and avoids allocation on the
// ISR-reachable path. See TD2 in net_tcp_overview.md §5.
const tcbMax = 16

var (
    tcbTable     [tcbMax]TCB
    tcbTableLock Spinlock // lock ordering rank 9
)
```

`TCB` is ~220 bytes; 16 slots = ~3.5 KiB for the table proper
plus 16 × (8 KiB tx + 8 KiB rx) = 256 KiB of per-TCB buffers
(see `net_tcp_buffers.md`).

### 2.1 TCB Allocation Rules

- `tcbAlloc(loc, rem)` scans the table under `tcbTableLock`,
  claims the first `!active` slot, zero-initializes its fields,
  sets `active = true`, and returns a pointer. Returns `nil` on
  exhaustion.
- `tcbFree(t)` runs under `tcbTableLock`, drains `t.rxBuf` and
  `t.txBuf`, cancels all timers (sets their deadlines to 0),
  and sets `active = false`.
- Lookup by 4-tuple: linear scan (16 entries × O(1) compare —
  acceptable at this table size). Lookup also accepts a
  "wildcard" local tuple used during LISTEN matching (see §6).

### 2.2 Lifetime

- Passive open: `tcbAlloc` happens when a SYN arrives on a
  LISTEN port — the TCB starts in `SYN_RECEIVED`.
- Active open: `tcbAlloc` happens at `sys_connect` — the TCB
  starts in `SYN_SENT`.
- Freeing: either `CLOSED` via the normal FIN handshake
  (`TIME_WAIT` expiry triggers `tcbFree`) or a RST/error path
  (immediate `tcbFree`).

---

## 3. State Machine Events

The event table is the authoritative contract for Phase TCP-1
and TCP-2. Each row must be covered by a test in
[`net_tcp_test_plan.md`](net_tcp_test_plan.md).

### 3.1 Legend

- **Seg.flag** columns use the RFC 793 abbreviations:
  `SYN`, `ACK`, `FIN`, `RST`, `URG`, `PSH`. `ACK-only` means
  ACK=1 and SYN=FIN=RST=0.
- **Syscall** events come from `sys_listen` / `sys_accept` /
  `sys_connect` / `sys_tcp_send` / `sys_tcp_recv` /
  `sys_shutdown` (see `net_tcp_socket_api.md §4`).
- **Timer** events are RTO, Persist, DelACK, TIMEWAIT,
  Connect.
- "Send X" actions build a segment via `tcpBuildSegment`
  (`net_tcp_segment_io.md §3`) and hand it to `ipv4Send`.

### 3.2 Transition table

| Current state | Event | Next state | Actions |
|---|---|---|---|
| CLOSED | syscall:listen | LISTEN | Allocate `tcpListener`; no TCB yet |
| CLOSED | syscall:connect | SYN_SENT | Alloc TCB; iss = isnNext(); send `SYN` with MSS option; set RTO, connectDeadline |
| CLOSED | seg:any | CLOSED | Send `RST` (unless arriving segment already has RST=1) |
| LISTEN | seg:SYN | SYN_RECEIVED | Alloc TCB for new 4-tuple; iss = isnNext(); irs = seg.seq; rcvNxt = irs+1; send `SYN|ACK` with MSS option; enqueue on listener's pending queue |
| LISTEN | seg:any other | LISTEN | Send `RST` |
| SYN_SENT | seg:SYN\|ACK (ack valid) | ESTABLISHED | irs = seg.seq; rcvNxt = irs+1; sndUna = seg.ack; cancel connectDeadline; send `ACK`; signal `rxWake` / userland to wake any waiter |
| SYN_SENT | seg:SYN (no ACK) | SYN_RECEIVED | Simultaneous open; send `SYN|ACK` (v1 may instead RST and fail — see §4) |
| SYN_SENT | seg:RST | CLOSED | `tcpError(t, fdErrBad)`; wake waiters; tcbFree |
| SYN_SENT | timer:connectDeadline | CLOSED | Retry per schedule; after 3 fails → tcbFree with error |
| SYN_RECEIVED | seg:ACK (valid, ack=sndNxt) | ESTABLISHED | Move TCB onto listener's accept queue (see §7); wake any `sys_accept` waiter |
| SYN_RECEIVED | seg:RST | CLOSED | tcbFree |
| SYN_RECEIVED | syscall:close | FIN_WAIT_1 | Send `FIN|ACK`; sndNxt++ |
| ESTABLISHED | seg:ACK-only (new data in segment) | ESTABLISHED | Copy payload into rxBuf; advance rcvNxt; send ACK (immediately if rxBuf past threshold, else start delack timer); wake `rxWake` |
| ESTABLISHED | seg:ACK-only (pure ACK) | ESTABLISHED | sndUna = seg.ack; update sndWnd, sndWl1, sndWl2; drop acked data from retxQ; update RTT; possibly wake `txWake` |
| ESTABLISHED | seg:FIN | CLOSE_WAIT | Advance rcvNxt past the FIN; send ACK; wake `rxWake` with EOF marker |
| ESTABLISHED | syscall:close | FIN_WAIT_1 | Drain queued data; send `FIN|ACK`; sndNxt++ |
| ESTABLISHED | timer:RTO | ESTABLISHED | Retransmit head of retxQ; back off RTO (×2, clamp 60 s); cwnd/ssthresh updates (`net_tcp_flow_and_congestion.md §6`) |
| ESTABLISHED | timer:DelACK | ESTABLISHED | Send pure ACK |
| FIN_WAIT_1 | seg:ACK (of our FIN) | FIN_WAIT_2 | sndUna = seg.ack; cancel RTO |
| FIN_WAIT_1 | seg:FIN | CLOSING | Send ACK |
| FIN_WAIT_1 | seg:FIN\|ACK (of our FIN) | TIME_WAIT | Send ACK; start timeWaitDeadline (60 s — TD5) |
| FIN_WAIT_2 | seg:FIN | TIME_WAIT | Send ACK; start timeWaitDeadline |
| CLOSING | seg:ACK (of our FIN) | TIME_WAIT | Start timeWaitDeadline |
| CLOSE_WAIT | syscall:close | LAST_ACK | Send `FIN|ACK`; sndNxt++ |
| LAST_ACK | seg:ACK (of our FIN) | CLOSED | tcbFree |
| TIME_WAIT | timer:TIMEWAIT | CLOSED | tcbFree |
| TIME_WAIT | seg:FIN | TIME_WAIT | Re-ACK; reset timeWaitDeadline |
| any | seg:invalid (bad checksum, ACK of future seq, etc.) | unchanged | Drop silently; bump `netStats.TcpInvalid` |

### 3.3 Unacceptable segment handling (RFC 793 §3.9)

A segment with any of:
- sequence number not in `[rcvNxt, rcvNxt + rcvWnd)` except for
  zero-length segments;
- ACK field outside `(sndUna, sndNxt]`;

is "unacceptable". Per RFC 793:
- If `RST = 1` → drop silently.
- Otherwise → send empty ACK (`seq = sndNxt`, `ack = rcvNxt`)
  and drop the payload. No state change.

This is implemented in a single helper `tcpRejectSegment(t,
hdr)` called from the head of every non-CLOSED / non-LISTEN
state handler.

---

## 4. Simultaneous Open

Per §3.2 row `SYN_SENT → seg:SYN (no ACK)`, v1 has two options:

- **Strict RFC 793**: transition to SYN_RECEIVED and send
  `SYN|ACK`. Requires shared-ISN coordination and bookkeeping.
- **v1 simplification**: send `RST`, free the TCB, fail the
  `connect()` syscall with `fdErrBad`.

**v1 decision**: the v1 implementation chooses the
simplification. Simultaneous open is rare in practice; the
strict handling is an easy post-v1 extension once the
machinery in the rest of the state machine is known-good. The
entry in the §3.2 table is to be annotated `(v1: RST+fail)` in
the implementation-time comment.

---

## 5. ISN Generation (iss)

RFC 793 §3.3 recommends a ~4-µs-period counter. gooos has
`pitTicks` at 100 Hz (10 ms). To approximate, use:

```go
// isnNext returns a 32-bit ISN. pitTicks is the kernel PIT
// counter; multiplying by 250000 gives a ~25 MHz-scaled clock
// — faster than the RFC recommendation, but fine for a hobby
// OS without an RNG. Per TD4 in net_tcp_overview.md §5.
func isnNext() uint32 {
    return uint32(pitTicks * 250000)
}
```

Notes:
- Predictable ISN is a known threat (TR10). gooos single-user
  threat model accepts the risk.
- Wraparound: the counter overflows every `2^32 / 100 * 1 s ≈
  497 days`, well beyond any practical gooos session.

---

## 6. Listen-Port Table

```go
const (
    tcpMaxListeners     = 4 // TD2
    tcpAcceptQueueDepth = 8 // TD2
)

// tcpListener — one per listening port. Protected by
// tcpListenLock (rank 10). See net_tcp_overview.md §10.
type tcpListener struct {
    port     uint16
    active   bool
    owner    int       // pid of process holding the listen fd

    // Pending queue: TCBs in SYN_RECEIVED waiting for their
    // third handshake ACK.
    pending  [tcpAcceptQueueDepth]*TCB
    nPending int

    // Accept queue: TCBs in ESTABLISHED waiting for a
    // sys_accept call to claim them.
    accept   [tcpAcceptQueueDepth]*TCB
    nAccept  int

    // One goroutine blocked in sys_accept waits on this.
    acceptWake chan struct{} // capacity 1
}

var (
    tcpListeners     [tcpMaxListeners]tcpListener
    tcpListenLock    Spinlock // rank 10
)
```

### 6.1 LISTEN matching

When a SYN arrives at `tcpHandle`:

1. Acquire `tcbTableLock` (rank 9). Linear-scan for an
   **existing** TCB matching the full 4-tuple. If found, hand
   off to the per-state handler of that TCB (covers retransmits
   of the initial SYN).
2. Still under `tcbTableLock`, lookup listener for
   `(localIP, localPort)`. If none → send RST.
3. Acquire `tcpListenLock` (rank 10 — always after 9).
4. If `nPending + nAccept == tcpAcceptQueueDepth` → send RST;
   do not create a TCB.
5. Otherwise, `tcbAlloc` a new TCB (still under rank 9 + 10),
   move into SYN_RECEIVED, append to `pending`.
6. Release rank 10 then rank 9.

### 6.2 Accept-queue promotion

When a TCB in SYN_RECEIVED receives its third-handshake ACK
(row `SYN_RECEIVED → seg:ACK`), the event handler:

1. Under `tcpListenLock` (rank 10, after rank 9), splices the
   TCB out of `listener.pending` and appends to `listener.accept`.
2. Non-blocking select-send on `listener.acceptWake` to wake
   any blocked `sys_accept`.

### 6.3 `sys_accept` handling

See `net_tcp_socket_api.md §4.3` for the full syscall handler.
The relevant state-machine interaction:

1. Acquire `tcpListenLock`.
2. If `nAccept > 0`: pop front of `accept[]`, release lock,
   allocate a fresh fd with a `socketFd` whose discriminant
   points at the TCB.
3. If `nAccept == 0`: release lock, block on
   `listener.acceptWake` with optional timeout; restart at step
   1 on wake.

---

## 7. Accept-Queue Rules

Invariants (enforced by assertions at the top of every
state-machine transition touching a listener):

- `0 ≤ nPending ≤ tcpAcceptQueueDepth`.
- `0 ≤ nAccept ≤ tcpAcceptQueueDepth`.
- `nPending + nAccept ≤ tcpAcceptQueueDepth` — combined cap.
- Every TCB in `pending[]` is in SYN_RECEIVED.
- Every TCB in `accept[]` is in ESTABLISHED (or later if the
  peer already sent FIN before `sys_accept` runs; that case
  still delivers the fd; the user sees EOF on first recv).

Violation of any invariant is a CRITICAL kernel bug and must
panic via `serialPanic("tcp: listener invariant: ...")`.

---

## 8. TCB Free Sequence

`tcbFree(t)` must be called with `tcbTableLock` held. It runs:

1. If `t.listener != nil` and `t.state != ESTABLISHED`,
   splice the TCB out of `listener.pending` or `listener.accept`.
2. Cancel all timers (`rtoDeadline = 0`, etc.).
3. Drop all descriptors from `t.retxQ`.
4. Drain `t.rxBuf` and `t.txBuf`.
5. Close `t.rxWake` / `t.txWake` (wakes any blocked
   `sys_tcp_recv` / `sys_tcp_send` with a no-bytes-available
   signal; they must check `t.state == CLOSED` and fail with
   `fdErrBad`).
6. Set `t.active = false`.

---

## 9. Integration Points

- **IP demux (T1)**: `src/ipv4.go:184-213` gets `case
  ipProtoTCP: tcpHandle(hdr, inner)` alongside the existing
  ICMP and UDP cases.
- **`tcpHandle`** (new, in `src/tcp.go`):
  1. Parse the segment (`tcpParse`, see `net_tcp_segment_io.md §2`).
  2. Verify checksum (`tcpChecksumVerify`).
  3. Acquire `tcbTableLock`.
  4. Lookup TCB by 4-tuple; if none, fall back to listener-match
     (§6.1).
  5. Dispatch to the state-specific handler table.
  6. Release `tcbTableLock`.
- **User goroutine syscalls**: the TCP syscalls (28-33,
  `net_tcp_socket_api.md §2`) all acquire `tcbTableLock` at the
  top of the handler, never the reverse order.

---

## 10. ISR / SMP Safety

- Every function that `tcpHandle` can call transitively must
  be on the safelist of `scripts/lint_isr.go` or mark its own
  body `//go:nosplit` if it only touches pre-allocated state.
  TCP state-machine helpers fall in the second category.
- No `make(chan ...)`, no `go` statement, no string concat, no
  `runtime.Gosched()` in the ISR path.
- Channel sends on `rxWake` / `txWake` / `acceptWake` are
  non-blocking `select` with `default:` (drop-on-full) —
  acceptable because the capacity is 1 and the peer-wake
  semantics are edge-triggered.
- Timer goroutines fire on `afterTicks` and re-enter the state
  machine by acquiring `tcbTableLock` from user-mode context,
  not from ISR context. This keeps the `tcbTableLock → rank 11`
  acquisition order clean.

---

## 11. LOC Estimate

| Component | Min | Max | File |
|---|---|---|---|
| TCB struct + tcbAlloc/tcbFree | 120 | 180 | `src/tcp.go` |
| State machine dispatch + event handlers | 250 | 400 | `src/tcp.go` |
| Listener table + accept queue | 80 | 130 | `src/tcp.go` |
| ISN generator | 10 | 20 | `src/tcp.go` |
| `tcpRejectSegment` helper | 20 | 40 | `src/tcp.go` |
| **Total (state-machine layer only)** | **480** | **770** | — |

Segment parse/build, retransmission queue, timers, flow/CC,
and buffers are counted separately in their own documents.

---

## 12. Verification Criteria

1. **Basic 3-way handshake (passive)**: `nc 127.0.0.1 10080`
   (hostfwd → guest 8080) completes SYN → SYN|ACK → ACK; pcap
   shows the three segments; TCB enters ESTABLISHED in serial
   log.
2. **Basic 3-way handshake (active)**: guest `tcpcli.elf
   10.0.2.2 10080` completes the handshake against a host
   server (`nc -l 10080`).
3. **FIN close from peer**: after `nc` closes, guest TCB
   transitions ESTABLISHED → CLOSE_WAIT → LAST_ACK → CLOSED in
   serial log.
4. **FIN close from guest**: guest `sys_shutdown` transitions
   ESTABLISHED → FIN_WAIT_1 → FIN_WAIT_2 → TIME_WAIT; 60 s
   later, tcbFree.
5. **Invalid segment rejection**: host sends a packet with
   bogus seq — guest responds with pure ACK (rcvNxt), state
   unchanged.
6. **RST from CLOSED**: host sends stray segment to a port with
   no listener — guest responds with RST.
7. **Accept-queue overflow**: 10 simultaneous SYNs to a listen
   port with depth 8 — first 8 get SYN|ACK, last 2 get RST.
8. **TCB exhaustion**: 17 simultaneous connections — 17th gets
   RST (passive) or `fdErrBad` from `sys_connect` (active).

---

## 13. Open Questions

1. **Simultaneous open**: §4 proposes v1 rejects with RST.
   Recommendation: accept this v1 simplification.
2. **Out-of-order segments**: v1 drops and relies on the peer's
   retransmission. Alternative: reassemble up to 4 segments in
   a per-TCB holding buffer. Recommendation: drop-in-v1, revisit
   only if host `curl` exhibits stalls.
3. **Cross-TCB fairness**: multiple active connections share
   `netBufLock` for TX. No fairness guarantee. Recommendation:
   accept FIFO semantics; revisit only if starvation is observed.

---

## 14. Relationship to Other Documents

- **`net_tcp_overview.md §5`**: Design Decisions TD2 (caps), TD4
  (ISN), TD5 (TIME_WAIT duration).
- **`net_tcp_segment_io.md`**: header parse/build referenced in
  §3 and §5 of this document.
- **`net_tcp_timers_and_rtt.md`**: timer fields in §2 TCB struct
  are detailed there.
- **`net_tcp_buffers.md`**: `txBuf` / `rxBuf` struct layout.
- **`net_tcp_flow_and_congestion.md`**: `sndWnd` / `rcvWnd` /
  `cwnd` / `ssthresh` / `dupAcks` usage.
- **`net_tcp_socket_api.md §4`**: syscalls that drive the
  machine through syscall events listed in §3.2.
