# Networking Stack — Per-TCB Send and Receive Buffers

Detailed design for the two ring buffers that every TCB owns:
`txBuf` (user bytes queued for transmission) and `rxBuf` (peer
bytes buffered until userspace reads them).

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Depends on: [`net_tcp_state_machine.md`](net_tcp_state_machine.md)
(TCB struct fields `txBuf` / `rxBuf`),
[`net_tcp_flow_and_congestion.md`](net_tcp_flow_and_congestion.md)
(how `txBuf` capacity feeds `cwnd` / `sndWnd` calculations
and how `rxBuf` capacity defines the advertised `rcvWnd`).

---

## 1. Goals

1. Provide bounded FIFO byte storage per connection, with O(1)
   push / peek / pop.
2. Keep total per-TCB buffer memory bounded across the 16-TCB
   cap (`net_tcp_overview.md §5 TD2 + TD6`).
3. Fit inside existing kernel-heap budget **without** borrowing
   from the 262 KiB `netbuf` pool (which is reserved for
   on-wire frames — see `net_tcp_overview.md §2` row "netbuf
   pool").
4. Zero heap allocation on the TX / RX hot paths; buffers are
   embedded in the `TCB` struct and allocated exactly once per
   TCB activation.

### 1.1 Non-goals

- Variable-capacity buffers. v1 uses fixed 8 KiB × 2 per TCB.
- Shared kernel-wide buffer pool with admission control. A
  per-TCB static allocation is simpler and predictable.
- Zero-copy DMA into user memory. The RX path copies through
  the ring; zero-copy is a post-v1 optimisation.

---

## 2. Why Not Reuse `netbuf`?

`src/netbuf.go:18-133` defines a 128 × 2048-byte pool used by
the NIC TX/RX descriptors. Three reasons TCP does **not**
reuse it for per-connection buffering:

1. **Lifetime mismatch.** A `netbuf` entry exists for the
   duration of one DMA transaction (< 1 ms). A TCP
   per-connection buffer exists for the lifetime of a
   connection (seconds to minutes). Holding a netbuf slot for
   that long starves the hot TX/RX path.
2. **Granularity mismatch.** `netbuf` is a 2048-byte slab; the
   per-TCB buffer is a byte-granular FIFO. Wrapping TCP data
   in 2 KiB chunks adds pointer chasing and segment
   fragmentation.
3. **Capacity collision.** 16 TCBs × 2 buffers × 2 KiB = 64 KiB
   — 25 % of the `netbuf` pool dedicated to idle connection
   storage. Unacceptable when a burst of received frames
   might need all 128 slots.

The per-TCB buffers instead live inline in the `TCB` struct
as fixed-size byte arrays (§3). Total footprint: 16 × (8 KiB
+ 8 KiB) = 256 KiB — kernel heap charge, no netbuf
contention.

---

## 3. Ring-Buffer Design

### 3.1 Struct

```go
const tcpRxBufSize = 8192
const tcpTxBufSize = 8192

// tcpRingBuf is a byte-granular FIFO. Power-of-two capacity
// so the modulo collapses to a bitwise AND.
type tcpRingBuf struct {
    data  [tcpRxBufSize]byte // note: same size used for both
                             // TX and RX rings; see §3.2 for
                             // the alternative of distinct sizes
    head  uint32             // read index modulo cap
    tail  uint32             // write index modulo cap
    count uint32             // bytes currently buffered
}
```

For the rare case where TX and RX want different capacities,
introduce two concrete ring types (`tcpTxRing` and
`tcpRxRing`) with distinct `[...]byte` field sizes. v1 keeps
them equal at 8 KiB.

### 3.2 Mask-based indexing

```go
const tcpBufMask = uint32(tcpRxBufSize - 1) // 0x1FFF
```

`head` and `tail` are **not** wrapped; they increment forever
and are masked at access time. This lets the ring distinguish
"full" from "empty" without a reserved slot:

- `count == 0` → empty.
- `count == tcpRxBufSize` → full.
- Peek at byte `i`: `data[(head + i) & tcpBufMask]`.
- Write at tail: `data[tail & tcpBufMask] = b; tail++`.

### 3.3 Operations

```go
// rbFree returns bytes available for writing.
func (r *tcpRingBuf) free() int {
    return tcpRxBufSize - int(r.count)
}

// rbLen returns bytes available for reading.
func (r *tcpRingBuf) len() int {
    return int(r.count)
}

// rbCap returns the total ring capacity.
func (r *tcpRingBuf) cap() int {
    return tcpRxBufSize
}

// rbWrite copies up to len(src) bytes into the ring. Returns
// actual bytes written (< len(src) when the ring fills).
func (r *tcpRingBuf) rbWrite(src []byte) int {
    n := len(src)
    if n > r.free() {
        n = r.free()
    }
    for i := 0; i < n; i++ {
        r.data[(r.tail + uint32(i)) & tcpBufMask] = src[i]
    }
    r.tail += uint32(n)
    r.count += uint32(n)
    return n
}

// rbRead copies up to len(dst) bytes out of the ring. Returns
// actual bytes read (< len(dst) when the ring empties).
func (r *tcpRingBuf) rbRead(dst []byte) int {
    n := len(dst)
    if n > r.len() {
        n = r.len()
    }
    for i := 0; i < n; i++ {
        dst[i] = r.data[(r.head + uint32(i)) & tcpBufMask]
    }
    r.head += uint32(n)
    r.count -= uint32(n)
    return n
}

// rbPeek copies `n` bytes starting at ring offset `off` into
// `dst` WITHOUT advancing head. Used by the retx queue to
// rebuild a segment from its descriptor.
func (r *tcpRingBuf) rbPeek(off, n uint32, dst []byte) {
    for i := uint32(0); i < n; i++ {
        dst[i] = r.data[(r.head + off + i) & tcpBufMask]
    }
}
```

### 3.4 Byte indexing ↔ sequence numbers (TX path)

The TX ring stores user bytes that have been enqueued but
not yet acknowledged. Mapping:

- `r.head & tcpBufMask` is the byte at `t.sndUna`.
- `r.tail & tcpBufMask` is the byte **after** the last byte
  added by `sys_tcp_send` (next write position).
- Bytes between `head` and `tail - 1` are either (a) in-flight
  (already sent, awaiting ACK) or (b) queued but not yet
  sent. The state machine tracks the boundary via `t.sndNxt`.

The retransmission queue entry (`tcpRetxEntry`, see
[`net_tcp_segment_io.md §5.1`](net_tcp_segment_io.md)) refers
to a window by `{seq, bufLen}`. `bufOff` (ring-buffer offset
relative to `sndUna`) is derived as `seq - t.sndUna`.

**Drop-on-ack** is a simple `rbRead`-equivalent advance of
`head` — bytes newly ACKed are no longer needed and the ring
space is returned to the user for more `sys_tcp_send` writes.

### 3.5 Byte indexing ↔ sequence numbers (RX path)

The RX ring stores peer bytes that arrived in-order but have
not yet been consumed by `sys_tcp_recv`:

- `r.head & tcpBufMask` is the oldest byte delivered to the
  user.
- `r.tail & tcpBufMask` is the next write position (the byte
  at `t.rcvNxt - 1` was the last one written).

The advertised `rcvWnd` is `r.free()` (modulo SWS
avoidance — see
[`net_tcp_flow_and_congestion.md §5`](net_tcp_flow_and_congestion.md)).

### 3.6 Out-of-order handling

Per `net_tcp_overview.md §14 Q2`, v1 drops out-of-order
segments. The RX ring therefore only ever accepts writes at
`rcvNxt`. A single integer `t.rcvNxt` fully describes the
expected sequence number; no reassembly buffer is needed.

---

## 4. Memory Layout and Sizing

### 4.1 Per-TCB cost

| Field | Bytes |
|---|---|
| 4-tuple + state + listener ptr | ~40 |
| SEND seq-space (sndUna / sndNxt / sndWnd / sndWl1 / sndWl2 / iss) | 24 |
| RECV seq-space (rcvNxt / rcvWnd / irs) | 12 |
| MSS local/peer/effective | 6 |
| RTT / RTO state (srtt / rttvar / rto) | 12 |
| CC state (cwnd / ssthresh / dupAcks / cwndAccum) | 13 |
| Timer deadlines (× 5) | 40 |
| Channel pointers (rxWake / txWake) | 16 |
| Bookkeeping (userOwner / active / goroutineRunning flags) | 8 |
| txBuf (`tcpRingBuf`, 8 KiB data + 12 B indices) | 8204 |
| rxBuf (same) | 8204 |
| retxQ (64 × 32 B descriptors + head/tail/n) | ~2052 |
| **TCB total** | **~18.6 KiB** |

### 4.2 Kernel-wide cost

16 TCBs × 18.6 KiB ≈ **298 KiB**. Fits comfortably inside the
kernel heap. The Ring-3 per-process heap cap (`userHeapLimit
= 2 MiB` in `src/process.go`) is irrelevant here — TCBs live
in kernel space — but the 2 MiB figure still bounds the
*envelope* of a single user program's TCP consumption once
accept'd fds accumulate: 16 TCBs × the SDK-side buffers they
imply never exceed it. The kernel heap (separate, multi-MiB)
has ample headroom.

Note on bookkeeping constants — the user address window
(`userAddrMin = 0x40000000`, `userAddrMax = 0x80000000`) at
`src/netsock.go:65-66` is what `userBufInRange` consults; it
is NOT the per-process heap cap. Do not confuse the two.

### 4.3 Why not 16 KiB per buffer?

Doubling buffer sizes would push per-TCB to ~35 KiB and the
16-TCB cap to ~560 KiB — still fits, but closer to approaching
the `netbuf` pool size (262 KiB). The 8 KiB choice matches the
Linux default (`net.ipv4.tcp_rmem`'s lowest bound) and gives
clear headroom for a second future concurrent subsystem
(e.g., virtio-net).

### 4.4 Static vs dynamic

Buffers are fixed-size arrays inside the TCB struct. The
`tcbTable[tcbMax]TCB` array therefore lives entirely in
`.bss` with zero allocation at runtime. This is both a
simplicity win (no `make([]byte, ...)` calls) and a
SMP-safety win (no GC concerns, even though gooos currently
runs TCP on BSP only).

---

## 5. Interaction with Retransmission Queue

The retransmission queue (`tcpRetxQueue`,
[`net_tcp_segment_io.md §5`](net_tcp_segment_io.md)) stores
segment descriptors, not payload copies. A retransmit
reconstructs the payload from `t.txBuf` using `rbPeek`:

```go
func retxReSend(t *TCB) {
    e := &t.retxQ.ring[t.retxQ.head & 0x3F]
    var payloadBuf [tcpMaxMSS]byte // stack-local
    t.txBuf.rbPeek(e.bufOff, uint32(e.bufLen), payloadBuf[:e.bufLen])
    tcpSendSegment(t, e.flags, payloadBuf[:e.bufLen])
    e.sentTicks = pitTicks
    e.xmitCount++
}
```

`tcpMaxMSS` is 1460 (Ethernet MTU - IP header - TCP header),
giving a 1460-byte stack buffer. Well under any kernel stack
size concern (Ring-0 stacks are 16 KiB).

When `retxAckTo(t, ack)` pops an entry, the sequence range
`[e.seq, e.endSeq)` has been acknowledged; no explicit action
on the ring is required (the `rbRead`-style advance of
`t.txBuf.head` happens implicitly because `t.sndUna` moves
forward, and the ring's effective "base" is pinned to
`sndUna`). The ring's `head` index is advanced in
`retxAckTo`:

```go
func retxAckTo(t *TCB, ack uint32) (newlyAcked uint32, ...) {
    // For every popped entry summing bytes acked:
    newlyAcked = ... // sum of payload lengths popped
    t.txBuf.head += newlyAcked
    t.txBuf.count -= newlyAcked
    // sndUna update happens in the caller.
    ...
}
```

---

## 6. Flow-Control Hooks

### 6.1 TX-side back-pressure

`sys_tcp_send` enters `tcpWriteFromUser`, which:

1. Copies into `t.txBuf` (up to `t.txBuf.free()` bytes).
2. Returns the written count.
3. If `t.txBuf.free() == 0` and not all user bytes were copied,
   the user-side SDK `TCPSendAll` loops via a new syscall
   invocation — the kernel never blocks a goroutine on an empty
   TX ring. (Short-write semantics, per
   `net_tcp_socket_api.md §11 Q1`.)

### 6.2 RX-side wakeup

When `tcpHandle` appends bytes to `t.rxBuf` via `rbWrite`, it
non-blocking-selects a token onto `t.rxWake`:

```go
select {
case t.rxWake <- struct{}{}:
default:
}
```

The capacity-1 channel is edge-triggered: a blocked
`sys_tcp_recv` unblocks on the token, re-checks the ring, and
returns bytes. If the ring fills without a waiter present, the
`default` branch drops the token — harmless, because the
next `sys_tcp_recv` will see non-empty state immediately.

### 6.3 Transmit-goroutine wakeup (retxQ drain)

Similarly, when `retxAckTo` drains the retx queue (and thus
the TX ring advances), `t.txWake` receives a non-blocking
token to wake any `sys_tcp_send` that left early due to a full
buffer. The user SDK's `TCPSendAll` loop implicitly relies on
this by re-issuing `sys_tcp_send`; no explicit blocking
needed.

---

## 7. Lock Ordering

Both `txBuf` and `rxBuf` operations happen exclusively under
`tcbTableLock` (rank 9). No sub-ranks are introduced for the
rings themselves.

---

## 8. LOC Estimate

| Component | Min | Max | File |
|---|---|---|---|
| `tcpRingBuf` struct + mask | 10 | 15 | `src/tcp.go` |
| `rbWrite` / `rbRead` / `rbPeek` | 40 | 70 | `src/tcp.go` |
| `free` / `len` / `cap` accessors | 10 | 15 | `src/tcp.go` |
| Ring-to-seq bookkeeping hooks | 20 | 30 | `src/tcp.go` |
| **Total** | **80** | **130** | — |

(LOC folded into the `src/tcp.go` totals in
`net_tcp_overview.md §8`. No dedicated file required; the
ring primitives live alongside the state machine for locality.)

---

## 9. Verification Criteria

1. **Empty / full invariants**: `rbWrite` of 8 KiB into a
   fresh ring returns 8192; a second `rbWrite` returns 0.
2. **Wrap correctness**: `rbWrite` 6 KiB, `rbRead` 4 KiB,
   `rbWrite` 6 KiB → final `count == 8192`, and reading out
   10 KiB yields exactly the bytes that were written in order.
3. **Peek non-destructive**: `rbPeek(0, 100, ...)` does not
   change `head` / `count`.
4. **Full-buffer short write from user**: user requests 100 KiB
   write via `TCPSend`; kernel returns 8192 on the first call
   (ring was empty), returns 0 on the second call (ring full),
   `TCPSendAll` loops until the peer ACKs and room opens up.
5. **Rcv-wnd shrink**: on receipt of 8 KiB from peer with no
   user reads, `rcvWnd` drops to 0 and subsequent segments are
   dropped (ACKed only).
6. **Rcv-wnd grow**: after user reads 4 KiB, `rcvWnd` becomes
   4 KiB and the next ACK carries the new value.
7. **Per-TCB memory**: `serialPrint` of `unsafe.Sizeof(TCB{})`
   is in the range 18-19 KiB.

---

## 10. Open Questions

1. **Distinct TX/RX sizes**: v1 makes both 8 KiB. Alternative:
   smaller TX (4 KiB) so senders feel back-pressure sooner.
   Recommendation: equal sizes — symmetrical, easier to
   reason about.
2. **Capacity scaling**: once v2 adds window scale, `rcvWnd`
   can exceed 64 KiB, but `rxBuf` would cap it. Recommendation:
   accept; revisit when window scale is added.
3. **Memory pressure handling**: if a future phase adds more
   concurrent subsystems, can the TCB-buffer static reservation
   be moved to a heap allocation? Recommendation: defer;
   static is fine at the 16-TCB cap.

---

## 11. Relationship to Other Documents

- **`net_tcp_overview.md §5`**: design decisions TD2 (16-TCB
  cap), TD6 (8 KiB buffer size).
- **`net_tcp_state_machine.md §2`**: TCB struct that embeds
  these ring buffers.
- **`net_tcp_segment_io.md §5.1`**: retransmission queue
  whose `bufOff`/`bufLen` index into `txBuf`.
- **`net_tcp_flow_and_congestion.md §2`**: consumer of
  `rxBuf.free()` for the advertised window.
- **`src/netbuf.go:18-133`**: the netbuf pool that is
  explicitly NOT reused; §2 justifies the separation.
