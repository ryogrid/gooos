# Networking Stack — Buffer Management, Statistics, and Diagnostics

Detailed design for Phase 4: packet buffer pool, network
statistics counters, interrupt-driven RX, diagnostic shell
command, and error handling.

Parent doc: `net_overview.md`.
Depends on: `net_ipv4_icmp_udp.md` (Phase 3).

---

## 1. Packet Buffer Pool (`src/netbuf.go`)

### 1.1 Motivation

Phases 1–3 use `make([]byte, N)` for packet construction and
reception, allocating from the TinyGo GC heap. This has two
problems:

1. **GC pressure**: Each packet allocates 1–2 KiB of garbage
   that the conservative GC must scan.
2. **ISR safety**: The RX interrupt handler must not allocate
   (TinyGo's `alloc` is not reentrant under `//go:nosplit`).

Phase 4 introduces a fixed-size pool of pre-allocated packet
buffers outside the GC heap.

### 1.2 Pool Design

```go
const (
    netBufCount = 128
    netBufSize  = 2048  // must match e1000BufSize for zero-copy RX
)

// netBufPool is the raw buffer storage. Allocated via
// allocPagesContig at init time. Each buffer is netBufSize
// bytes, so total = 128 * 2048 = 262,144 bytes = 64 pages.
// Serves BOTH RX (zero-copy descriptor buffers) and TX
// (ethernetBuild uses pool buffers instead of make()).
var netBufPoolBase uintptr

// netBufFree is a bitmap: bit i=1 means buffer i is free.
// 128 bits = 2 uint64 words.
var netBufFree [2]uint64

var netBufLock Spinlock  // lock ordering rank 5
```

### 1.3 Initialization

```go
func netBufInit() {
    netBufPoolBase = allocPagesContig(48) // 48 * 4096 = 196608

    // Mark all buffers as free
    netBufFree[0] = ^uint64(0) // bits 0-63
    netBufFree[1] = ^uint64(0) // bits 64-127
}
```

### 1.4 Allocate

```go
// netBufAlloc returns a pointer to a free 1536-byte buffer,
// or 0 if none available. The caller owns the buffer until
// netBufFreeIdx is called.
func netBufAlloc() (uintptr, int) {
    flags := netBufLock.Acquire()
    for w := 0; w < 2; w++ {
        if netBufFree[w] != 0 {
            // Find lowest set bit
            bit := ctz64(netBufFree[w])
            netBufFree[w] &^= 1 << bit
            idx := w*64 + int(bit)
            netBufLock.Release(flags)
            addr := netBufPoolBase + uintptr(idx)*netBufSize
            return addr, idx
        }
    }
    netBufLock.Release(flags)
    netStats.BufAllocFail++
    return 0, -1
}
```

**`ctz64` (Count Trailing Zeros):**
```go
func ctz64(x uint64) int {
    if x == 0 {
        return 64
    }
    n := 0
    if x&0x00000000FFFFFFFF == 0 { n += 32; x >>= 32 }
    if x&0x000000000000FFFF == 0 { n += 16; x >>= 16 }
    if x&0x00000000000000FF == 0 { n += 8;  x >>= 8  }
    if x&0x000000000000000F == 0 { n += 4;  x >>= 4  }
    if x&0x0000000000000003 == 0 { n += 2;  x >>= 2  }
    if x&0x0000000000000001 == 0 { n += 1 }
    return n
}
```

### 1.5 Free

```go
// netBufFreeIdx returns buffer idx to the pool.
func netBufFreeIdx(idx int) {
    if idx < 0 || idx >= netBufCount {
        return
    }
    flags := netBufLock.Acquire()
    w := idx / 64
    bit := uint(idx % 64)
    netBufFree[w] |= 1 << bit
    netBufLock.Release(flags)
}
```

### 1.6 Slice Helper

```go
// netBufSlice returns a byte slice over buffer idx with the
// given length. The underlying storage is identity-mapped and
// outside the GC heap.
func netBufSlice(idx int, length int) []byte {
    addr := netBufPoolBase + uintptr(idx)*netBufSize
    return (*[netBufSize]byte)(unsafe.Pointer(addr))[:length:netBufSize]
}
```

### 1.7 Zero-Copy RX Path

With the buffer pool, the RX path becomes:

1. ISR signals RX goroutine.
2. RX goroutine calls `netBufAlloc()` → gets buffer index.
3. Copies packet from RX descriptor buffer into pool buffer
   (or directly uses the pool buffer as the RX descriptor
   buffer — requires pool buffers to be 16-byte aligned and
   physically contiguous, which they are).
4. Passes `(idx, length)` to Ethernet dispatch.
5. After processing, calls `netBufFreeIdx(idx)`.

**Optimization**: Point RX descriptors directly at pool
buffers, **replacing** the separate `rxBufs` allocation from
Phase 1 (those pages are freed). This eliminates the copy.
On RX, the buffer is "claimed" from the pool, and a new
buffer is allocated and assigned to the descriptor.

### 1.8 LOC Estimate

| Item | LOC |
|---|---|
| Constants + pool storage | 15–20 |
| `netBufInit` | 10–15 |
| `netBufAlloc` + `ctz64` | 30–45 |
| `netBufFreeIdx` | 10–15 |
| `netBufSlice` | 8–12 |
| **Total** | **73–107** |

---

## 2. Statistics Counters (`src/netstats.go`)

### 2.1 Counter Structure

```go
type NetStats struct {
    // Link layer
    TxPackets          uint64
    TxBytes            uint64
    RxPackets          uint64
    RxBytes            uint64
    RxDropped          uint64
    RxUnknownEtherType uint64

    // ARP
    ArpHits            uint64
    ArpMisses          uint64
    ArpRepliesSent     uint64
    ArpRequestsSent    uint64

    // IPv4
    ChecksumErr        uint64
    FragmentsDropped   uint64

    // ICMP
    IcmpEcho           uint64

    // UDP
    UdpRecv            uint64
    UdpSend            uint64
    UdpPortUnreach     uint64

    // Buffer pool
    BufAllocFail       uint64
}

var netStats NetStats
var statsLock Spinlock  // lock ordering rank 8
```

### 2.2 Increment Patterns

For counters incremented from a single goroutine (the RX
dispatch loop), no lock is needed — goroutines in TinyGo
are cooperative, and the RX loop runs on one CPU.

For counters incremented from ISR context or multiple
goroutines (e.g., `TxPackets` from any goroutine calling
`e1000Transmit`), use atomic-style increment:

```go
// statsInc atomically increments a counter. Since TinyGo does
// not have sync/atomic, we use cli/sti bracketing or spinlock.
func statsInc(counter *uint64) {
    flags := statsLock.Acquire()
    *counter++
    statsLock.Release(flags)
}
```

**Note**: For single-goroutine counters, direct `netStats.X++`
is safe. Only multi-producer counters need `statsInc`.

### 2.3 Counter Access (for diagnostics)

```go
// netStatsSnapshot copies the current stats under lock.
func netStatsSnapshot() NetStats {
    flags := statsLock.Acquire()
    snap := netStats
    statsLock.Release(flags)
    return snap
}
```

### 2.4 LOC Estimate

| Item | LOC |
|---|---|
| Struct definition | 20–30 |
| `statsInc` | 5–8 |
| `netStatsSnapshot` | 8–12 |
| **Total** | **33–50** |

---

## 3. Interrupt-Driven RX

### 3.1 Replacing Polling

Phase 1 uses polling with `hlt()` between checks. Phase 4
replaces this with an interrupt-driven model:

```go
var rxSignalCh chan struct{}

func e1000RxInit() {
    rxSignalCh = make(chan struct{}, 4)
}
```

### 3.2 Updated RX Loop

```go
func netRxLoop() {
    for {
        // Wait for interrupt signal
        <-rxSignalCh

        // Drain all ready RX descriptors
        for {
            frame := e1000TryReceive()
            if frame == nil {
                break
            }
            netStats.RxPackets++
            netStats.RxBytes += uint64(len(frame))
            ethernetDispatch(frame)
        }
    }
}
```

### 3.3 Updated IRQ Handler

```go
//go:nosplit
func handleE1000IRQ(vector uint64) {
    icr := e1000Read(e1000ICR)

    if icr & e1000ICRRXT0 != 0 {
        // Non-blocking signal to RX goroutine
        select {
        case rxSignalCh <- struct{}{}:
        default: // already signaled, skip
        }
    }

    if icr & e1000ICRLSC != 0 {
        // Link status change
        if e1000Read(e1000STATUS) & e1000StatusLU != 0 {
            serialPrintln("e1000: link up (IRQ)")
        } else {
            serialPrintln("e1000: link down (IRQ)")
        }
    }

    // Re-arm interrupts (ICR read-to-clear)
    // No need to re-write IMS; reading ICR auto-clears causes.

    if ioapicActive {
        lapicSendEOI()
    } else {
        picSendEOI(uint8(vector - 32))
    }
}
```

### 3.4 LOC Estimate

| Item | LOC |
|---|---|
| `rxSignalCh` + init | 5–8 |
| Updated `netRxLoop` | 15–25 |
| Updated IRQ handler | 25–35 |
| **Total** | **45–68** |

---

## 4. Diagnostic Output

### 4.1 Serial-Based Diagnostics

A `netDiag()` function prints all network state to serial:

```go
func netDiag() {
    serialPrintln("=== Network Diagnostics ===")

    // Link status
    status := e1000Read(e1000STATUS)
    if status & e1000StatusLU != 0 {
        serialPrintln("Link: UP")
    } else {
        serialPrintln("Link: DOWN")
    }

    // MAC address
    serialPrintln("MAC: " + macToString(e1000MAC))

    // IP configuration
    serialPrintln("IP:  " + ipToString(ourIP))
    serialPrintln("GW:  " + ipToString(ourGateway))

    // ARP cache dump
    serialPrintln("ARP cache:")
    flags := arpLock.Acquire()
    for i := 0; i < arpCacheSize; i++ {
        if arpCache[i].Used {
            serialPrintln("  " + ipToString(arpCache[i].IP) +
                " -> " + macToString(arpCache[i].MAC))
        }
    }
    arpLock.Release(flags)

    // Statistics
    snap := netStatsSnapshot()
    serialPrintln("TX: " + utoa(snap.TxPackets) + " pkts, " +
        utoa(snap.TxBytes) + " bytes")
    serialPrintln("RX: " + utoa(snap.RxPackets) + " pkts, " +
        utoa(snap.RxBytes) + " bytes")
    serialPrintln("RX dropped: " + utoa(snap.RxDropped))
    serialPrintln("ARP: " + utoa(snap.ArpHits) + " hits, " +
        utoa(snap.ArpMisses) + " misses")
    serialPrintln("ICMP echo: " + utoa(snap.IcmpEcho))
    serialPrintln("UDP: " + utoa(snap.UdpRecv) + " recv, " +
        utoa(snap.UdpSend) + " send")
    serialPrintln("Checksum errors: " + utoa(snap.ChecksumErr))
    serialPrintln("Buf alloc fails: " + utoa(snap.BufAllocFail))
}
```

### 4.2 Userland `netstat.elf` (Future)

A userspace `netstat` command would use a new syscall
(`sys_netstat` or similar) to retrieve the `NetStats` struct.
This is deferred until the socket syscall API is designed.

For Phase 4, the kernel calls `netDiag()` periodically or on
demand via a keyboard shortcut (e.g., Ctrl+N) or at boot after
traffic.

### 4.3 Boot-Time Diagnostic

After a brief delay (e.g., 500 ticks = 5 seconds), print
diagnostics automatically:

```go
go func() {
    <-afterTicks(500) // 5 seconds after boot
    netDiag()
}()
```

### 4.4 LOC Estimate

| Item | LOC |
|---|---|
| `netDiag` function | 40–60 |
| Boot-time diagnostic | 5–10 |
| **Total** | **45–70** |

---

## 5. Error Handling and Edge Cases

### 5.1 Frame Validation

In `ethernetDispatch`:
```go
// Drop runt frames (< 60 bytes, minimum Ethernet frame)
if len(frame) < 60 {
    netStats.RxDropped++
    return
}

// Drop oversized frames (> 1518 bytes)
if len(frame) > 1518 {
    netStats.RxDropped++
    return
}
```

### 5.2 IPv4 Validation (already in `ipv4Parse`)

- Version ≠ 4 → drop
- IHL < 5 → drop
- Checksum mismatch → drop + counter
- Fragment (MF=1 or offset≠0) → drop + counter
- TTL = 0 → drop (no ICMP Time Exceeded)

### 5.3 UDP Validation

- Length < 8 → drop
- Data length > declared length → truncate
- Checksum mismatch (if non-zero) → drop + counter
- No binding for port → drop silently

### 5.4 TX Validation

- Frame > 1518 bytes → reject (`e1000Transmit` returns false)
- Frame < 14 bytes → reject
- TX ring full → spin-wait with timeout → drop + counter

### 5.5 Buffer Pool Exhaustion

When `netBufAlloc` fails (all 128 buffers in use):
- Increment `BufAllocFail` counter
- Drop the incoming packet
- Log warning to serial (once per N failures to avoid flood)

### 5.6 LOC Estimate

| Item | LOC |
|---|---|
| Frame validation checks | 15–25 |
| TX validation | 10–15 |
| Buffer exhaustion handling | 10–15 |
| **Total** | **35–55** |

---

## 6. Memory Budget

| Allocation | Size | Source |
|---|---|---|
| RX descriptor ring (64 × 16) | 1,024 B = 1 page | `allocPagesContig(1)` |
| TX descriptor ring (32 × 16) | 512 B = 1 page | `allocPagesContig(1)` |
| RX buffers (64 × 2,048) | 131,072 B = 32 pages | `allocPagesContig(32)` |
| TX buffers (32 × 2,048) | 65,536 B = 16 pages | `allocPagesContig(16)` |
| Packet buffer pool (128 × 2,048) | 262,144 B = 64 pages | `allocPagesContig(64)` |
| ARP cache (16 × ~20) | 320 B | `.bss` |
| UDP bind table (8 × ~32) | 256 B | `.bss` |
| Statistics counters | ~128 B | `.bss` |
| **Total** | **~394 KiB** | 98 pages bump-allocated |

This is well within the ~950 MiB of available memory
(identity-mapped 1 GiB minus kernel + heap).

---

## 7. Verification Criteria

### 7.1 Buffer Pool

1. `netBufInit()` completes without panic.
2. Allocate 128 buffers → all succeed.
3. Allocate 129th → returns 0 (failure).
4. Free one buffer → next alloc succeeds.
5. No memory leak after 10,000 alloc/free cycles.

### 7.2 Statistics

1. After `ping` exchange, `RxPackets` and `TxPackets` > 0.
2. After bad-checksum packet, `ChecksumErr` > 0.
3. `netDiag()` prints all counters without panic.

### 7.3 Interrupt-Driven RX

1. Boot with `-device e1000`.
2. Send `ping` → kernel replies without polling loop.
3. `rxSignalCh` receives signals from ISR.
4. RX loop drains all descriptors per interrupt.

### 7.4 Error Handling

1. Send runt frame (< 60 B) → dropped, `RxDropped` incremented.
2. Send oversized frame → dropped.
3. Send fragmented IPv4 → `FragmentsDropped` incremented.
4. Send UDP to unbound port → dropped silently.

---

## 8. Risks Specific to Phase 4

| Risk | Mitigation |
|---|---|
| Buffer pool exhaustion under flood | 128 buffers = ~192 KiB; sufficient for non-flood scenarios. Drop + counter on exhaustion. Increase pool size if needed. |
| `ctz64` implementation error | Test with known input values (0, 1, 0x8000000000000000, 0xFFFFFFFFFFFFFFFF). |
| Spinlock contention on `statsLock` | Most counters incremented from single goroutine (RX loop); only TX counters need lock. Consider per-counter atomic if contention observed. |
| ISR `select` on channel not ISR-safe | TinyGo `select` on buffered channel with `default` is safe in `//go:nosplit` context (no allocation, no scheduler call on non-blocking path). Verify by testing. |
| netBufSlice returns raw pointer slice | The slice header references memory outside GC heap. Conservative GC will not collect it (it's not on the GC heap). Safe. |
