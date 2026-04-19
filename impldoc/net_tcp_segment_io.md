# Networking Stack — TCP Segment I/O

Detailed design for the wire-format side of TCP: header parse
and build, pseudo-header checksum, the MSS option (the only
option negotiated in v1), and the retransmission queue that
holds unacknowledged segment descriptors.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Companion: [`net_tcp_state_machine.md`](net_tcp_state_machine.md).

---

## 1. Goals

1. Parse and build the 20-byte fixed TCP header plus up to
   20 bytes of options (40 total). Mirror the style of
   `src/udp.go` (`udpParse`, manual byte-level assembly).
2. Compute the TCP checksum over the pseudo-header + header +
   payload — reuse the ones-complement loop from
   `src/udp.go:69-104` (`udpChecksum`). Handle odd-length
   payloads the same way UDP does.
3. Emit **only the MSS option** on outbound SYN segments. Parse
   incoming options defensively (skip unknown options; reject
   malformed TLVs with a hard iteration cap).
4. Maintain a bounded **retransmission queue** per TCB as a
   ring of segment descriptors (not raw frames), so a TCB's
   memory footprint stays predictable.

### 1.1 Non-goals

- SACK blocks (RFC 2018), timestamps (RFC 7323), window scale
  (RFC 7323), any experimental options.
- Urgent-pointer semantics beyond preserving the field
  verbatim (gooos does not raise SIGURG).

---

## 2. Wire Layout

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Source Port          |       Destination Port        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Sequence Number                        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Acknowledgment Number                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  DOff | Rsrvd |U|A|P|R|S|F|            Window                 |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|            Checksum           |         Urgent Pointer        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Options (0-40 bytes)                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                            Payload                            |
+...
```

- **DOff** (Data Offset): 4 bits. Header length in 32-bit
  words. Min 5 (20 B), max 15 (60 B).
- **Reserved**: 4 bits, always zero on send; ignored on receive.
- **Flags** (6 bits): `URG`, `ACK`, `PSH`, `RST`, `SYN`, `FIN`.
  No ECN (CWR, ECE), no NS in v1.
- **Window**: advertised receive window (16 bits, unscaled).
- **Checksum**: 16 bits, RFC 793 — over pseudo-header + TCP
  header + payload.
- **Urgent Pointer**: preserved on forward, never generated.

### 2.1 Constants

```go
const (
    tcpHeaderMinSize = 20 // no options
    tcpHeaderMaxSize = 60 // with 40 B options

    // Flag bitmasks (byte 13 of the header).
    tcpFlagFIN = uint8(0x01)
    tcpFlagSYN = uint8(0x02)
    tcpFlagRST = uint8(0x04)
    tcpFlagPSH = uint8(0x08)
    tcpFlagACK = uint8(0x10)
    tcpFlagURG = uint8(0x20)

    // Option kinds we recognize.
    tcpOptEnd    = uint8(0)
    tcpOptNop    = uint8(1)
    tcpOptMSS    = uint8(2) // len = 4
    // Known-but-ignored:
    tcpOptWScale = uint8(3)
    tcpOptSACKok = uint8(4)
    tcpOptSACK   = uint8(5)
    tcpOptTS     = uint8(8)

    tcpDefaultMSS = uint16(536) // RFC 1122 default (TD3)
)
```

---

## 3. Parse and Build

### 3.1 Parsed form

```go
type TCPHeader struct {
    SrcPort uint16
    DstPort uint16
    Seq     uint32
    Ack     uint32
    DataOff uint8  // 4 bits; header length = DataOff*4 bytes
    Flags   uint8  // FIN|SYN|RST|PSH|ACK|URG
    Window  uint16
    Chksum  uint16
    Urgent  uint16
    // Raw option bytes (0-40). Parsed separately via tcpParseOptions.
    Options [40]byte
    OptLen  uint8
}
```

### 3.2 `tcpParse`

```go
// tcpParse splits a TCP segment into (header, payload). Returns
// ok=false for any of: truncated header, DataOff < 5, DataOff*4
// exceeds packet length, options length > 40. Does NOT verify
// checksum — call tcpChecksumVerify separately.
func tcpParse(packet []byte) (TCPHeader, []byte, bool) {
    if len(packet) < tcpHeaderMinSize {
        return TCPHeader{}, nil, false
    }
    h := TCPHeader{
        SrcPort: uint16(packet[0])<<8 | uint16(packet[1]),
        DstPort: uint16(packet[2])<<8 | uint16(packet[3]),
        Seq: uint32(packet[4])<<24 | uint32(packet[5])<<16 |
             uint32(packet[6])<<8 | uint32(packet[7]),
        Ack: uint32(packet[8])<<24 | uint32(packet[9])<<16 |
             uint32(packet[10])<<8 | uint32(packet[11]),
        DataOff: packet[12] >> 4,
        Flags:   packet[13] & 0x3F,
        Window:  uint16(packet[14])<<8 | uint16(packet[15]),
        Chksum:  uint16(packet[16])<<8 | uint16(packet[17]),
        Urgent:  uint16(packet[18])<<8 | uint16(packet[19]),
    }
    if h.DataOff < 5 {
        return h, nil, false
    }
    hdrLen := int(h.DataOff) * 4
    if hdrLen > tcpHeaderMaxSize || hdrLen > len(packet) {
        return h, nil, false
    }
    optLen := hdrLen - tcpHeaderMinSize
    if optLen > 0 {
        copy(h.Options[:optLen], packet[tcpHeaderMinSize:hdrLen])
        h.OptLen = uint8(optLen)
    }
    return h, packet[hdrLen:], true
}
```

### 3.3 `tcpBuildSegment`

```go
// tcpBuildSegment composes a TCP segment (header + payload) into
// `out`. Caller has already ensured out has capacity for the
// header + options + len(payload). Returns the total length
// written. The Chksum field is left zero; callers fill it after
// a tcpChecksum pass. Options must be a valid (pre-padded to a
// 4-byte boundary) options blob or nil.
func tcpBuildSegment(out []byte,
    srcPort, dstPort uint16,
    seq, ack uint32,
    flags uint8,
    window uint16,
    options []byte,
    payload []byte) int {

    optLen := len(options)
    if optLen%4 != 0 || optLen > 40 {
        return 0 // programmer error: caller must pad options
    }
    hdrLen := tcpHeaderMinSize + optLen
    total := hdrLen + len(payload)
    if len(out) < total {
        return 0
    }
    out[0] = byte(srcPort >> 8)
    out[1] = byte(srcPort)
    out[2] = byte(dstPort >> 8)
    out[3] = byte(dstPort)
    out[4] = byte(seq >> 24)
    out[5] = byte(seq >> 16)
    out[6] = byte(seq >> 8)
    out[7] = byte(seq)
    out[8] = byte(ack >> 24)
    out[9] = byte(ack >> 16)
    out[10] = byte(ack >> 8)
    out[11] = byte(ack)
    out[12] = byte((hdrLen / 4) << 4)
    out[13] = flags & 0x3F
    out[14] = byte(window >> 8)
    out[15] = byte(window)
    out[16] = 0 // Chksum placeholder
    out[17] = 0
    out[18] = 0 // Urgent
    out[19] = 0
    if optLen > 0 {
        copy(out[20:20+optLen], options)
    }
    copy(out[hdrLen:], payload)
    return total
}
```

### 3.4 Option parsing (RX only)

```go
// tcpParseOptions walks the 0-40 option bytes. Returns the MSS
// advertised by the peer (or tcpDefaultMSS if none). Unknown
// options are skipped. Malformed TLVs (length < 2, length
// running off the end) cause early return with peerMSS =
// tcpDefaultMSS and ok = false.
//
// The iteration cap (40) is a hard upper bound; the loop counter
// must never rely solely on offset arithmetic in case of bogus
// lengths. See risk TR11 in net_tcp_overview.md §9.
func tcpParseOptions(opts []byte) (peerMSS uint16, ok bool) {
    peerMSS = tcpDefaultMSS
    i := 0
    for iter := 0; iter < 40 && i < len(opts); iter++ {
        kind := opts[i]
        switch kind {
        case tcpOptEnd:
            return peerMSS, true
        case tcpOptNop:
            i++
            continue
        }
        if i+1 >= len(opts) {
            return tcpDefaultMSS, false
        }
        length := int(opts[i+1])
        if length < 2 || i+length > len(opts) {
            return tcpDefaultMSS, false
        }
        if kind == tcpOptMSS && length == 4 {
            peerMSS = uint16(opts[i+2])<<8 | uint16(opts[i+3])
        }
        // Unknown / ignored options fall through to skip.
        i += length
    }
    return peerMSS, true
}
```

### 3.5 Building outbound options

v1 emits options **only** on SYN and SYN|ACK segments, and only
the MSS option:

```go
// tcpBuildMSSOption fills `out[0:4]` with an MSS option carrying
// `mss`. Returns 4. Caller passes `out[4:8]` to emit a trailing
// End-of-Option / padding pair if needed (we prefer a two-NOP
// pad to keep scan-loop invariants simple).
//
// Layout: [kind=2][len=4][MSS hi][MSS lo]
func tcpBuildMSSOption(out []byte, mss uint16) int {
    out[0] = tcpOptMSS
    out[1] = 4
    out[2] = byte(mss >> 8)
    out[3] = byte(mss)
    return 4
}
```

The outbound options blob on SYN / SYN|ACK is exactly four
bytes — already 4-byte aligned — so no NOP/END padding is
necessary. v1 never emits options on non-SYN segments.

---

## 4. Pseudo-Header Checksum

The pseudo-header is the same 12-byte layout UDP uses (see
`src/udp.go:69-104`, reproduced in `net_tcp_overview.md §2`):

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Source Address                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Destination Address                      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|      Zero      |   Protocol   |           TCP Length          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

### 4.1 `tcpChecksum`

```go
// tcpChecksum computes the RFC 793 checksum over the pseudo-
// header + tcpPacket (header with Chksum=0 plus payload).
// Returns the one's-complement value, NEVER translated to
// 0xFFFF — TCP, unlike UDP, does not have the "0 means disabled"
// convention. Zero is a legal on-wire value that means "the
// ones-complement sum was all ones" (extremely rare) but is
// still valid and must be transmitted verbatim.
func tcpChecksum(srcIP, dstIP uint32, tcpPacket []byte) uint16 {
    var pseudo [12]byte
    pseudo[0] = byte(srcIP >> 24)
    pseudo[1] = byte(srcIP >> 16)
    pseudo[2] = byte(srcIP >> 8)
    pseudo[3] = byte(srcIP)
    pseudo[4] = byte(dstIP >> 24)
    pseudo[5] = byte(dstIP >> 16)
    pseudo[6] = byte(dstIP >> 8)
    pseudo[7] = byte(dstIP)
    pseudo[8] = 0
    pseudo[9] = ipProtoTCP
    l := uint16(len(tcpPacket))
    pseudo[10] = byte(l >> 8)
    pseudo[11] = byte(l)

    sum := uint32(0)
    for i := 0; i+1 < len(pseudo); i += 2 {
        sum += uint32(pseudo[i])<<8 | uint32(pseudo[i+1])
    }
    i := 0
    for ; i+1 < len(tcpPacket); i += 2 {
        sum += uint32(tcpPacket[i])<<8 | uint32(tcpPacket[i+1])
    }
    if i < len(tcpPacket) {
        sum += uint32(tcpPacket[i]) << 8 // odd byte padded
    }
    for sum>>16 != 0 {
        sum = (sum & 0xFFFF) + (sum >> 16)
    }
    return ^uint16(sum)
}
```

### 4.2 `tcpChecksumVerify`

```go
// tcpChecksumVerify returns true when the checksum field in
// `packet` matches a recomputed value. Unlike UDP, a zero
// checksum is NOT a hint to skip verification — we must still
// recompute and compare.
func tcpChecksumVerify(srcIP, dstIP uint32, packet []byte) bool {
    if len(packet) < tcpHeaderMinSize {
        return false
    }
    wire := uint16(packet[16])<<8 | uint16(packet[17])
    scratch := make([]byte, len(packet))
    copy(scratch, packet)
    scratch[16] = 0
    scratch[17] = 0
    return tcpChecksum(srcIP, dstIP, scratch) == wire
}
```

**Scratch allocation note**: `tcpChecksumVerify` allocates a
short-lived byte slice; this happens on the RX path
(`tcpHandle`). Since `tcpHandle` runs from the `netRxLoop`
goroutine (not directly in ISR context — see
`net_tcp_state_machine.md §10`), the allocation is allowed.
If the ISR lint flags this, the alternative is to XOR the
on-wire checksum bytes into the running sum and subtract (see
a future optimization doc); do not prematurely optimize.

### 4.3 `tcpComputeAndSetChecksum`

Convenience wrapper called by every send path after building
the segment:

```go
func tcpComputeAndSetChecksum(srcIP, dstIP uint32,
    packet []byte) {
    c := tcpChecksum(srcIP, dstIP, packet)
    packet[16] = byte(c >> 8)
    packet[17] = byte(c)
}
```

---

## 5. Retransmission Queue

Per-TCB ring of **segment descriptors**, not full frames. On a
retransmit the descriptor is converted back into a wire frame
on-the-fly. Saves memory (per TD11 in `net_tcp_overview.md §5`).

### 5.1 Descriptor

```go
const tcpRetxMax = 64 // per TCB; bounded to avoid memory blow-up

type tcpRetxEntry struct {
    seq       uint32 // first sequence number in this segment
    endSeq    uint32 // seq + payload length + SYN/FIN flag accounting
    flags     uint8  // retransmit with these control flags
    bufOff    uint32 // byte offset into txBuf where payload starts
    bufLen    uint16 // payload length (0 for SYN-only / FIN-only)
    sentTicks uint64 // pitTicks at last (re)send — for RTT sample
    xmitCount uint8  // 0 on first send; bumped on retransmit
}

type tcpRetxQueue struct {
    ring [tcpRetxMax]tcpRetxEntry
    head uint8 // read index (oldest unacked)
    tail uint8 // write index (next free)
    n    uint8 // count in [0, tcpRetxMax]
}
```

Index arithmetic uses `uint8 % tcpRetxMax` = `uint8 & 63` since
`tcpRetxMax` is a power of two (keep the cap a power of two
for this simplification).

### 5.2 Invariants

- `q.n <= tcpRetxMax`.
- If `q.n == 0`, `q.head == q.tail`.
- Entries are stored in ascending `seq` order (TCP never
  reorders its own send).
- `q.ring[q.head].seq == t.sndUna` whenever `q.n > 0`.

Violation panics: `serialPanic("tcp: retx invariant: ...")`.

### 5.3 Operations

- `retxPush(t *TCB, e tcpRetxEntry)` — enqueue at tail. If full
  returns false; the send path treats this as "stall"
  (`sys_tcp_send` blocks on `txWake` until retxPop advances).
- `retxAckTo(t *TCB, ack uint32)` — pop entries whose
  `endSeq <= ack`. Returns the count popped, the oldest
  `sentTicks` (for RTT sampling), and whether ANY entry with
  `xmitCount == 0` was popped (for Karn's rule — only
  pristine entries feed the RTT estimator).
- `retxHead(t *TCB) *tcpRetxEntry` — peek without popping; used
  by the RTO timer to retransmit the head.
- `retxReSend(t *TCB)` — rebuild the head's wire form from the
  descriptor + txBuf bytes, bump `sentTicks = pitTicks`,
  `xmitCount++`, call `ipv4Send`.

### 5.4 Sequence accounting for control flags

`endSeq` is computed at enqueue time:

- SYN: consumes 1 sequence number, payload 0.
- FIN: consumes 1 sequence number, payload 0.
- Pure ACK / data: consumes `bufLen` sequence numbers.
- SYN + payload (unusual, but RFC 793 permits it): 1 + bufLen.
- FIN + payload (used for combined close + trailing write):
  bufLen + 1.

The formula:
```go
endSeq = seq + uint32(bufLen)
if flags & tcpFlagSYN != 0 { endSeq++ }
if flags & tcpFlagFIN != 0 { endSeq++ }
```

---

## 6. Send Path

`tcpSendSegment(t *TCB, flags uint8, payload []byte)` —
single entry point used by the state machine.

1. Resolve next-hop MAC via `arpResolve(gateway or remoteIP)`
   (re-use `src/arp.go:220-240`).
2. Build header into a stack-local array: 20 B + optional
   4 B MSS option (on SYN).
3. Call `tcpBuildSegment(...)`, then `tcpComputeAndSetChecksum(...)`.
4. Call `ipv4Send(ipProtoTCP, t.remoteIP, packet)` — the
   3-argument form at `src/ipv4.go:154`. Source IP is
   implicit (`ourIP`); TCB's `t.localIP` is identity-only and
   is **not** passed to `ipv4Send` (v1 never has a TCB whose
   `localIP` differs from `ourIP`).
5. Enqueue a `tcpRetxEntry` unless:
   - The segment is a **pure ACK** (no SYN, no FIN, no payload
     beyond 0 bytes).
   - The segment is a **RST** — never retransmitted (RFC 793).

### 6.1 MSS option emission rules

- On **SYN** (SYN_SENT): include the MSS option. Value is
  `t.mssLocal` = `tcpDefaultMSS` in v1 (no PMTUD).
- On **SYN|ACK** (SYN_RECEIVED): include the MSS option. Value
  is `min(t.mssLocal, t.mssPeer)` = `t.mssEff`.
- On all other segments: no options.

### 6.2 Effective MSS for data transmission

`t.mssEff` is set to `min(t.mssLocal, t.mssPeer)` the first time
a SYN or SYN|ACK is received; afterwards, `tcpSendSegment`
never emits a payload larger than `t.mssEff - 40` if options
present (but options are never present on data segments, so
effectively `t.mssEff`).

---

## 7. Receive Path

`tcpHandle(hdr IPv4Header, inner []byte)` — called from
`src/ipv4.go:184-213` with the inner (post-IP) bytes.

1. Reject if `len(inner) < tcpHeaderMinSize`.
2. `tcpParse(inner)` — reject on failure, bump
   `netStats.TcpInvalid`.
3. `tcpChecksumVerify(hdr.SrcIP, hdr.DstIP, inner)` — reject
   on failure, bump `netStats.TcpChecksumErr`.
4. If the parsed header carries a SYN (anywhere in the
   3-way handshake), call `tcpParseOptions(h.Options[:h.OptLen])`
   to extract the peer MSS.
5. Acquire `tcbTableLock`.
6. Lookup by 4-tuple `{hdr.DstIP, h.DstPort, hdr.SrcIP,
   h.SrcPort}`. If found, dispatch to the per-state handler
   (see `net_tcp_state_machine.md §3.2`).
7. If not found but `h.Flags` has `SYN` set, fall through to
   listener-match (`net_tcp_state_machine.md §6.1`).
8. If still no match: if `h.Flags & tcpFlagRST == 0`, send a
   RST back at `{seq = h.Ack, ack = h.Seq + segLen}` with
   `ACK` flag only if the incoming segment had `ACK = 1`
   (RFC 793 §3.4). Otherwise drop silently.

`segLen` accounting = payload bytes + (SYN set ? 1 : 0) +
(FIN set ? 1 : 0).

---

## 8. Integration Points

- **`src/ipv4.go:20`** — add `ipProtoTCP = uint8(6)` next to
  the existing `ipProtoICMP` / `ipProtoUDP` constants.
- **`src/ipv4.go:184-213`** — in the `switch hdr.Protocol`
  block, insert:
  ```go
  case ipProtoTCP:
      tcpHandle(hdr, inner)
  ```
  between the existing ICMP and UDP cases (order does not
  matter, but keep it before the final `default:` drop).
- **`src/tcp_segment.go`** — new file, ~320 LOC, contains
  `tcpParse`, `tcpBuildSegment`, `tcpParseOptions`,
  `tcpBuildMSSOption`, `tcpChecksum`, `tcpChecksumVerify`,
  `tcpComputeAndSetChecksum`, and the retransmission queue
  methods.
- **No edits** to `src/udp.go`. The checksum pattern is
  copy-paste-adapted (different protocol constant, different
  "zero checksum" semantics); code reuse across UDP and TCP
  would require a generic helper that is not justified at
  2 call sites.

---

## 9. ISR / SMP Safety

- `tcpHandle` runs from `netRxLoop`, not directly from the
  e1000 ISR. It **may** allocate (via `make([]byte, ...)` in
  `tcpChecksumVerify`) but should keep allocations small and
  short-lived. If `make lint` objects, replace with a static
  per-CPU scratch buffer.
- `tcpBuildSegment` writes into a stack-local array supplied
  by the caller — no heap allocation on the send path.
- `tcpParseOptions` has a hard iteration cap (40) and bailouts
  on any malformed TLV; it cannot loop.

---

## 10. LOC Estimate

| Component | Min | Max | File |
|---|---|---|---|
| Header parse + build | 80 | 120 | `src/tcp_segment.go` |
| Option parse + MSS build | 40 | 80 | `src/tcp_segment.go` |
| Pseudo-header checksum + verify | 40 | 60 | `src/tcp_segment.go` |
| Retransmission queue ops | 60 | 100 | `src/tcp_segment.go` |
| **Total** | **220** | **360** | — |

---

## 11. Verification Criteria

1. **Header round-trip**: build a segment with every flag set,
   parse it back, compare field-for-field.
2. **Checksum match**: a known-good pcap TCP segment (e.g.,
   captured from `tcpdump -w` of Linux-to-Linux traffic) passes
   `tcpChecksumVerify`. One bit flipped anywhere in the
   segment fails it.
3. **MSS option parse**: a SYN with the MSS option set to 1460
   yields `peerMSS == 1460`. A SYN with no options yields
   `peerMSS == 536`.
4. **Malformed option**: a SYN with `[kind=2][len=0]` yields
   `ok == false, peerMSS == 536`. Loop terminates.
5. **Odd-length payload**: a segment with 13-byte payload and a
   known checksum passes verify.
6. **Retransmission queue FIFO**: push 3 entries, ack the
   middle — `retxAckTo` pops only the first; `retxHead` now
   points at entry 2.
7. **Retransmission queue full**: push 64 entries, attempt 65th
   — returns false.

---

## 12. Open Questions

1. **Odd-byte checksum**: the code pads with `0x00` in the
   high byte of the final `uint16`. Confirm this matches the
   existing `udpChecksum` behaviour at `src/udp.go:93-95`
   before committing. Recommendation: mirror UDP exactly
   (already done in the draft).
2. **Checksum-verify scratch allocation**: §4.2 allocates a
   new byte slice. Alternative: XOR-subtract the checksum
   bytes from a single-pass running sum. Recommendation: keep
   the simpler version for v1; revisit if the ISR lint flags
   it.
3. **Retransmission-queue cap 64**: sized for 16 × 64 = 1024
   entries kernel-wide, ~48 KiB total. Acceptable; raise only
   if `iperf3` saturates it.

---

## 13. Relationship to Other Documents

- **`net_tcp_overview.md §5` (Design Decisions)**: TD3 (MSS
  default), TD11 (retx queue is descriptors not frames).
- **`net_tcp_state_machine.md §3`** — the state machine
  calls `tcpSendSegment` (§6) and `tcpHandle` (§7) defined
  here.
- **`net_tcp_timers_and_rtt.md`** — RTO consumer of
  `retxHead` and `retxReSend`; RTT sampling uses
  `tcpRetxEntry.sentTicks` with Karn's rule.
- **`net_tcp_buffers.md`** — the per-TCB `txBuf` is the byte
  source `tcpRetxEntry.bufOff` / `bufLen` index into.
- **`src/udp.go:69-104`** — template for the checksum loop.
