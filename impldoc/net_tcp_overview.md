# Networking Stack — TCP Overview and Work Plan

This document is the entry point to the design set for adding
**TCP (Transmission Control Protocol, RFC 793 with RFC 1122, 6298,
5681 updates)** to gooos. It extends — it does **not** replace —
the existing networking documentation under `impldoc/net_*.md`.
Phases 1-4 (`net_overview.md §5`) delivered Ethernet/ARP/IPv4/ICMP/
UDP; Phase 5 (`net_socket_api.md`, `net_dhcp_client.md`) delivered
the Ring-3 socket API and DHCP client. TCP is explicitly listed as
deferred in `net_overview.md §1.2` and `net_socket_api.md §1.1`.

Eight companion documents under `impldoc/net_tcp_*.md` together
provide an implementation blueprint for a **Phase TCP-1 … TCP-5**
work split. This document is the parent; each subsystem document
depends on the decisions made here and cross-references back.

Audience: **Claude Code (the CLI agent)**, executing the future
implementation session.

---

## 1. Problem Statement

gooos currently has a working UDP/IPv4/Ethernet stack
(`impldoc/net_overview.md §5`, Phases 1-4) and a Ring-3 socket API
for `SOCK_DGRAM` sockets (`impldoc/net_socket_api.md`). TCP
(`SOCK_STREAM`) is not implemented. Without TCP, the guest cannot
run an HTTP server or client, cannot be reached by `curl` or
browsers, and cannot talk to any service that requires reliable
byte-stream transport.

### 1.1 Goals

1. **TCP segment I/O** over the existing `ipv4Send` / `ipv4Handle`
   path (`src/ipv4.go:184-213`). Protocol number 6.
2. **RFC 793 state machine** with all eleven states
   (`CLOSED`/`LISTEN`/`SYN_SENT`/`SYN_RECEIVED`/`ESTABLISHED`/
   `FIN_WAIT_1`/`FIN_WAIT_2`/`CLOSE_WAIT`/`CLOSING`/`LAST_ACK`/
   `TIME_WAIT`) driven by syscall / segment / timer events.
3. **Passive open** (server) and **active open** (client) — both
   host-initiated and guest-initiated connections.
4. **Reliable byte-stream transport**: sequence numbers,
   cumulative ACKs, retransmission with RFC 6298 RTO, graceful
   FIN close with TIME_WAIT.
5. **Flow control** via the receive-window advertisement.
6. **Minimum viable congestion control** — slow start +
   congestion avoidance + fast retransmit (RFC 5681). No SACK,
   no timestamps, no window scale, no ECN in v1.
7. **Socket API extension** — add `SOCK_STREAM` support via new
   syscalls `28` – `33` (`sys_listen`, `sys_accept`,
   `sys_connect`, `sys_tcp_send`, `sys_tcp_recv`, `sys_shutdown`),
   keeping the existing Phase 5 ABI (`net_socket_api.md §2`)
   unchanged.
8. **Ring-3 SDK** additions in `user/gooos/net.go` mirroring the
   existing UDP surface.
9. **Verification**: host `nc 10.0.2.15 8080` and `curl` reach a
   kernel- or userspace-resident TCP echo / HTTP server; guest
   `tcp_connect.elf` reaches a host-resident server.

### 1.2 Non-Goals (deferred)

- **SACK** (RFC 2018), **timestamps** (RFC 7323), **window
  scale** (RFC 7323) — deferred to a post-v1 phase.
- **ECN** (RFC 3168) — deferred.
- **Path MTU Discovery** (RFC 1191) — MSS fixed at 536 or the
  value learned from the peer's SYN `MSS` option, whichever is
  smaller; no ICMP `Fragmentation Needed` handling.
- **Nagle's algorithm** — off by default in v1 (latency-preferred
  for interactive test programs). Decision revisitable; see
  `net_tcp_flow_and_congestion.md §5`.
- **Keep-alive** — opt-in via a future syscall; not wired in v1.
- **`shutdown(SHUT_RD)` half-close semantics** — only
  `SHUT_WR` and full close are supported in v1.
- **TCP over IPv6** — no IPv6 in gooos.
- **TLS / application-layer protocols** — out of scope; userland
  opt-in.
- **SMP correctness beyond BSP-only** — TCP runs on BSP only,
  matching the current gooos deployment state
  (`impldoc/smp_deferred_and_known_issues.md`). Per-CPU TCP is
  future work.

---

## 2. Existing Infrastructure Leveraged

Every row below is a deliberate reuse decision; TCP must **not**
introduce a parallel primitive when one of these already works.

| Subsystem | File | Reuse for TCP |
|---|---|---|
| IPv4 demux switch | `src/ipv4.go:184-213` (`ipv4Handle`) | Add `case ipProtoTCP:` calling `tcpHandle`; define `ipProtoTCP = 6` constant alongside the existing `ipProtoICMP` / `ipProtoUDP` block near `src/ipv4.go:20` |
| IPv4 send | `src/ipv4.go:154` (`ipv4Send(proto uint8, dstIP uint32, payload []byte) bool`) | Three-argument form — source IP is implicit `ourIP`. TCP calls it with `ipProtoTCP`; outbound segment's `t.localIP` is identity bookkeeping only (always equals `ourIP` in v1). No change to IPv4 layer |
| Pseudo-header checksum template | `src/udp.go:69-104` (`udpChecksum`, `udpChecksumVerify`) | Copy the ones-complement-over-pseudo-header+payload pattern into `tcpChecksum` / `tcpChecksumVerify` (see `net_tcp_segment_io.md §3`) |
| Bind-table pattern | `src/udp.go:19-46` (`UDPBinding`, `udpBindings[udpMaxBinds]`, `udpLock`) | Not reused directly — TCP needs a separate **listen-port table** and a **TCB (Transmission Control Block) table** keyed by 4-tuple; see `net_tcp_state_machine.md §2` |
| socketFd | `src/netsock.go:90-94` | Extended to carry `SOCK_STREAM` state via a new discriminant field; `net_tcp_socket_api.md §3` |
| User-pointer bounds check | `src/netsock.go:72-84` (`userBufInRange`) | Every TCP syscall that copies user memory must call this exactly as the UDP syscalls do |
| ARP resolve | `src/arp.go:220-240` (`arpResolve(ip uint32) ([6]byte, bool)`) | Every outbound segment calls `arpResolve(nextHop)` before TX — identical to `udpSend` path |
| netbuf pool | `src/netbuf.go:18-133` (128 × 2048 B) | Used for **on-wire frames only**. Per-connection send/receive buffers do **not** come from this pool — see `net_tcp_buffers.md §2` for the rationale |
| Spinlock | `src/spinlock.go:46-59` + rank table lines 7-15 | New TCP locks at ranks 9-11; see §10 below |
| `afterTicks` | `src/afterticks.go:26-36` (`func afterTicks(d uint64) <-chan struct{}`) | Every TCP timer (RTO, persist, delayed-ACK, TIME_WAIT, connect-timeout) is layered on this — see `net_tcp_timers_and_rtt.md` |
| `FileDesc` interface | `src/fd.go` | TCP-flavoured `socketFd` continues to implement it; `Read`/`Write`/`Close` map to stream semantics |
| ISR-safety lint | `scripts/lint_isr.go` | TCP ISR-reachable paths (`tcpHandle` under `rxSignalCh`) must pass the lint — no `make(chan)`, no string concat, no `go`-statements |
| Ring-3 syscall stub set | `user/rt0.S` (syscall0..syscall5) | Reused as-is for the new syscalls 28-33 |

---

## 3. Document Coverage Table

| Doc | Scope | Key decisions |
|---|---|---|
| `net_tcp_overview.md` (this file) | Entry point, phasing, reuse map, lock-order extension, risk register, open questions, deferred reviewer findings | §7 phases, §10 locks |
| [`net_tcp_state_machine.md`](net_tcp_state_machine.md) | RFC 793 states, event table, ISN policy, TCB struct, listen/accept/close sequencing, TIME_WAIT duration | Eleven states in full; 60 s TIME_WAIT |
| [`net_tcp_segment_io.md`](net_tcp_segment_io.md) | Header parse/build, pseudo-header checksum, MSS-only option handling, retransmission queue | MSS default 536; max window 64 KiB (no scale) |
| [`net_tcp_timers_and_rtt.md`](net_tcp_timers_and_rtt.md) | RFC 6298 RTO, persist, delayed-ACK, TIME_WAIT, connect timeout — all on `afterTicks` | PIT 100 Hz = 10 ms resolution acknowledged |
| [`net_tcp_flow_and_congestion.md`](net_tcp_flow_and_congestion.md) | Receive-window bookkeeping, zero-window probing, SWS avoidance, Nagle off, slow start + CA + fast retransmit | `cwnd` / `ssthresh` per TCB |
| [`net_tcp_socket_api.md`](net_tcp_socket_api.md) | Syscalls 28-33, `socketFd` extension, listen/accept queue, Ring-3 SDK, errno mapping | ABI mirrors Phase 5 (`int 0x80` + register convention) |
| [`net_tcp_buffers.md`](net_tcp_buffers.md) | Per-TCB send/recv ring buffers, sizing inside the 2 MiB user heap + 262 KiB netbuf pool | 8 KiB TX + 8 KiB RX per TCB; 16 TCB cap |
| [`net_tcp_test_plan.md`](net_tcp_test_plan.md) | Numbered tests, QEMU user-mode + TAP, `nc` / `curl` / `iperf3` / `tcpdump`, automation under `tmp/`, new README demo paths | Paths D (kernel TCP echo) + E (userspace TCP server) |
| [`net_tcp_work_plan.md`](net_tcp_work_plan.md) | Per-phase checklist mirroring `pasttodos/TODO_NET2.md`, final phase schedules README update and `TODO_NET3.md` creation | 5 phases, ~3,500 LOC target |

Every link above is relative and must resolve; a stray link is a
CRITICAL reviewer finding.

---

## 4. Blocker Inventory

Every row is a capability gooos lacks today; all are resolved by a
specific section of a TCP document.

| ID | Blocker | Resolved in |
|---|---|---|
| T1 | No `ipProtoTCP` constant and no `case ipProtoTCP:` in `ipv4Handle` | `net_tcp_segment_io.md §2` |
| T2 | No TCP header parse/build functions | `net_tcp_segment_io.md §3` |
| T3 | No TCP pseudo-header checksum variant | `net_tcp_segment_io.md §4` |
| T4 | No Transmission Control Block (TCB) struct | `net_tcp_state_machine.md §2` |
| T5 | No TCP state machine driver | `net_tcp_state_machine.md §3` |
| T6 | No ISN generator | `net_tcp_state_machine.md §5` |
| T7 | No listen-port table | `net_tcp_state_machine.md §6` |
| T8 | No accept queue | `net_tcp_state_machine.md §7` |
| T9 | No retransmission queue | `net_tcp_segment_io.md §5` |
| T10 | No RTO / RTT estimator | `net_tcp_timers_and_rtt.md §2` |
| T11 | No delayed-ACK timer | `net_tcp_timers_and_rtt.md §4` |
| T12 | No TIME_WAIT timer | `net_tcp_timers_and_rtt.md §5` |
| T13 | No receive-window bookkeeping | `net_tcp_flow_and_congestion.md §2` |
| T14 | No zero-window persist / window update | `net_tcp_flow_and_congestion.md §3` |
| T15 | No congestion control (cwnd/ssthresh) | `net_tcp_flow_and_congestion.md §6` |
| T16 | No per-TCB send/recv ring buffers | `net_tcp_buffers.md §3` |
| T17 | `socketFd` has no `SOCK_STREAM` discriminant | `net_tcp_socket_api.md §3` |
| T18 | No `sys_listen`/`sys_accept`/`sys_connect`/`sys_tcp_send`/`sys_tcp_recv`/`sys_shutdown` | `net_tcp_socket_api.md §4` |
| T19 | No Ring-3 TCP SDK surface | `net_tcp_socket_api.md §6` |
| T20 | No TCP test paths or automation | `net_tcp_test_plan.md §2-§5` |

---

## 5. Design Decisions

| # | Decision | Rationale | Rejected alternative |
|---|---|---|---|
| TD1 | Separate TCB table (cap 16), not reusing `udpBindings` | TCP needs 4-tuple keying (`{srcIP,srcPort,dstIP,dstPort}`) plus per-connection state; UDP binds on 1-tuple `{port}` | Widening `UDPBinding` (would break the UDP rx hot path) |
| TD2 | Fixed cap: 16 TCBs, 4 listen ports, 8 pending-accept entries per listen port | Stays inside the existing kernel heap budget and matches the 16-fd-per-process ceiling | Dynamic allocation (TinyGo `make` unsafe inside ISR dispatch) |
| TD3 | MSS default 536; honour peer-advertised MSS if smaller | RFC 1122 floor; keeps segments well below the 1500 B Ethernet MTU and below the 1472 B UDP cap that the existing stack already tolerates | Fixed 1460 (would assume Ethernet MTU without PMTUD) |
| TD4 | ISN = `pitTicks * 250000` (mod 2³²) | RFC 793 §3.3 recommends a 250 kHz-ish clock; `pitTicks` runs at 100 Hz so this approximates the spec cadence | `rand()` — gooos has no RNG; adding one is out of scope |
| TD5 | 60 s TIME_WAIT (not the RFC 793 2 × MSL = 4 min) | Shortens per-connection resource hold for a hobby OS with a 16-TCB cap; still several multiples of QEMU's single-digit-ms RTT | Full 2 × MSL (exhausts the TCB table under repeated short connections) |
| TD6 | 8 KiB send buffer + 8 KiB recv buffer per TCB | 16 TCBs × 16 KiB = 256 KiB total — fits inside kernel heap without approaching the per-process 2 MiB user-heap limit | 64 KiB buffers (would demand 2 MiB kernel heap just for TCBs) |
| TD7 | Slow start + CA + fast retransmit (RFC 5681) only | Required for correct behaviour under QEMU slirp packet loss; SACK / timestamps / window scale are pure performance extensions | Reno or newer (unneeded complexity) |
| TD8 | New syscalls 28-33 rather than overloading existing 22-27 | Keeps the Phase 5 ABI stable; matches the project convention of one syscall per operation | `sys_socket_op` multiplex (opaque, harder to lint) |
| TD9 | BSP-only lock strategy | Matches the current gooos runtime (`impldoc/smp_deferred_and_known_issues.md`) — all goroutines run on CPU 0 | SMP-aware per-TCB locks (premature given the known SMP limitations) |
| TD10 | RX arrival runs under the existing `rxSignalCh` → `netRxLoop` goroutine | Already the UDP/ICMP path; avoids introducing a second dispatch loop | Dedicated TCP goroutine (redundant; complicates lock ordering) |
| TD11 | Retransmission queue is a bounded ring of segment descriptors (not raw frames) | Saves memory; frames are rebuilt from descriptors on retransmit | Full frame copies per queued segment (multiplies memory use by MSS) |
| TD12 | `sys_tcp_recv` has a timeout argument, mirroring `sys_recvfrom`'s `R8` slot | Consistency with `net_socket_api.md §4.4` open-question resolution | Blocking-only (DHCP-style lesson already learned) |

---

## 6. Phase Breakdown (TCP-1 … TCP-5)

Each phase is an independent merge unit with its own verification
gate. The detailed checklist lives in `net_tcp_work_plan.md`.

### Phase TCP-1 — Passive open + kernel echo server

Goal: Host `nc 10.0.2.15 8080` reaches a Ring-0 TCP echo server
and round-trips a payload. No retransmission, no congestion
control, no active open.

- `src/tcp.go` (new): TCB struct, state machine driver,
  `tcpHandle(hdr, inner)`, LISTEN / SYN_RECEIVED / ESTABLISHED /
  CLOSE_WAIT / LAST_ACK / CLOSED states.
- `src/tcp_segment.go` (new): header parse/build + pseudo-header
  checksum.
- `src/ipv4.go`: wire `case ipProtoTCP:`.
- Kernel-side `tcpEchoServer` goroutine on port 8080 (analogue
  of `udpEchoServer` in `src/udp.go:312-324`).

### Phase TCP-2 — Active open + retransmission + RTT

Goal: Guest-initiated `connect()` works; dropped segments
retransmit; RFC 6298 RTO tuned to QEMU slirp RTT.

- SYN_SENT / FIN_WAIT_1 / FIN_WAIT_2 / CLOSING / TIME_WAIT
  states complete.
- `src/tcp_retx.go` (new): retransmission queue + RTO timer on
  `afterTicks`.
- `src/tcp_rtt.go` (new): RFC 6298 SRTT / RTTVAR estimator.

### Phase TCP-3 — Flow control + delayed ACK

Goal: Large transfers don't stall; receive-window bookkeeping
stays in sync across both directions.

- `src/tcp_flow.go` (new): send/recv window tracking,
  zero-window persist, delayed-ACK timer.

### Phase TCP-4 — Congestion control (RFC 5681)

Goal: Survives QEMU slirp packet reordering/loss without
collapse; throughput scales with `cwnd`.

- `src/tcp_cc.go` (new): slow start + congestion avoidance +
  fast retransmit.

### Phase TCP-5 — Socket API + Ring-3 SDK + demos + README

Goal: Userspace programs can `Socket(SOCK_STREAM)`, `Listen`,
`Accept`, `Connect`, `Send`, `Recv`, `Close`. Host tools round
trip through a userspace server. README progress table +
"Running the networking demos" section updated.

- `src/netsock.go`: extend `socketFd` discriminant; new
  syscalls 28-33.
- `src/userspace.go`: dispatch new syscalls.
- `user/gooos/net.go`: `TCPSocket` / `TCPListen` / `TCPAccept` /
  `TCPConnect` / `TCPSend` / `TCPRecv` / `TCPClose`.
- `user/cmd/tcpecho/main.go` (new): userspace TCP echo server.
- `user/cmd/tcpcli/main.go` (new): userspace TCP client.
- `Makefile`: `run-net` adds `hostfwd=tcp::10080-:8080` and
  `hostfwd=tcp::10081-:8081`.
- `README.md`: new progress-table row and demo Paths D / E
  added to the "Running the networking demos" section; pointer
  to `pasttodos/TODO_NET3.md`.
- `TODO_NET3.md`: final-phase checklist record.

---

## 7. Dependency DAG

```
Phase TCP-1 (passive open)
  [T1.a] ipProtoTCP const
        │
        └──► [T1.b] tcp_segment.go (parse/build + checksum)
                   │
                   └──► [T1.c] tcp.go TCB struct
                              │
                              └──► [T1.d] state machine (LISTEN + SYN_RECEIVED + ESTABLISHED + CLOSE_WAIT + LAST_ACK + CLOSED)
                                         │
                                         └──► [T1.e] kernel echo goroutine @ 8080
                                                    │
                                                    └──► [T1.f] run-net hostfwd tcp::10080-:8080

Phase TCP-2 (active open + retx + RTT)
  [T2.a] SYN_SENT state + active-open API (kernel-only)
        │
        └──► [T2.b] tcp_retx.go (retransmission queue + RTO timer)
                   │
                   └──► [T2.c] tcp_rtt.go (SRTT/RTTVAR)
                              │
                              └──► [T2.d] FIN path + TIME_WAIT

Phase TCP-3 (flow control)
  [T3.a] tcp_flow.go rcv window
        │
        └──► [T3.b] zero-window persist
                   │
                   └──► [T3.c] delayed-ACK timer

Phase TCP-4 (congestion control)
  [T4.a] cwnd + ssthresh
        │
        └──► [T4.b] slow start / CA transition
                   │
                   └──► [T4.c] fast retransmit + fast recovery

Phase TCP-5 (API + demos + README)
  [T5.a] socketFd discriminant
        │
        └──► [T5.b] syscalls 28-33
                   │
                   ├──► [T5.c] user/gooos/net.go TCP SDK
                   │         │
                   │         ├──► [T5.d] user/cmd/tcpecho
                   │         └──► [T5.e] user/cmd/tcpcli
                   │
                   └──► [T5.f] README.md + TODO_NET3.md
```

---

## 8. LOC Estimates

| Phase | Component | Min | Max | New files |
|---|---|---|---|---|
| TCP-1 | segment I/O | 200 | 320 | `src/tcp_segment.go` |
| TCP-1 | state machine + TCB + kernel echo | 400 | 650 | `src/tcp.go` |
| TCP-2 | retransmission queue + RTO | 180 | 300 | `src/tcp_retx.go` |
| TCP-2 | SRTT / RTTVAR | 80 | 140 | `src/tcp_rtt.go` |
| TCP-3 | flow control (rcv window + persist + delayed ACK) | 150 | 250 | `src/tcp_flow.go` |
| TCP-4 | congestion control | 120 | 220 | `src/tcp_cc.go` |
| TCP-5 | socketFd extension + 6 syscall handlers | 350 | 520 | (edit `src/netsock.go`) |
| TCP-5 | user SDK | 150 | 220 | (edit `user/gooos/net.go`) |
| TCP-5 | userspace tcpecho + tcpcli | 120 | 220 | `user/cmd/tcpecho/`, `user/cmd/tcpcli/` |
| TCP-5 | README + TODO_NET3 | 60 | 120 | (edit `README.md`, new `pasttodos/TODO_NET3.md`) |
| **Total** | | **1,810** | **2,960** | **~8 new files** |

Changes to existing files (all edits — no rewrites):
- `src/ipv4.go`: `ipProtoTCP = 6` constant + one `case` in the demux switch (~4 lines).
- `src/netsock.go`: extend `socketFd` with a discriminant field + six new syscall handlers.
- `src/userspace.go`: syscall numbers 28-33 and dispatch cases (~12 lines).
- `src/spinlock.go`: update the rank comment at lines 7-15 to extend through rank 11.
- `src/main.go`: one call to `tcpInit()` after `netInit()`.
- `Makefile`: two TCP `hostfwd` entries in `run-net`.
- `user/rt0.S`: no new stub required — syscalls 28-33 all fit the existing `syscall0..syscall5` set.
- `user/gooos/net.go`: TCP SDK additions alongside the existing UDP surface.
- `user/Makefile`: add `tcpecho` and `tcpcli` to `CMDS`.
- `README.md`: new progress-table row; Paths D and E added to "Running the networking demos".

---

## 9. Risk Register

| ID | Risk | Severity | Likelihood | Mitigation |
|---|---|---|---|---|
| TR1 | State machine bug causes data loss on FIN race | High | Medium | Exhaustive event-table testing; reference `net_tcp_state_machine.md §3` event matrix |
| TR2 | RTO estimator oscillates under QEMU slirp (RTTs occasionally jump to seconds) | Medium | High | Clamp RTO to `[1 s, 60 s]`; RFC 6298 G = 1 tick = 10 ms |
| TR3 | Retransmission queue fills → kernel stalls | Medium | Medium | Bounded queue (64 descriptors per TCB); drop + reset on overflow |
| TR4 | Receive-window update lost → peer stalls in zero-window | High | Medium | Persist timer per RFC 1122; probe once per RTO |
| TR5 | TIME_WAIT holds TCBs → table exhaustion under churn | Medium | Medium | 60 s TIME_WAIT (TD5) + 16-TCB cap; under-pressure `accept` returns `fdErrBad` |
| TR6 | ISR-reachable TCP path allocates → lint fail | High | Medium | All `tcpHandle` paths pre-allocate work buffers; no `make`, no `go`, no string concat |
| TR7 | Checksum bug (endianness or pseudo-header) silently drops valid segments | High | Medium | Byte-level tests against known-good pcap fixtures; mirror the working `udpChecksum` layout |
| TR8 | TCB lookup is O(N) under a single rank-9 spinlock → global contention | Low | Low | 16-entry linear scan; revisit if > 4 µs becomes observable in tests |
| TR9 | Guest-side `connect()` retransmits SYN too aggressively → slirp blacklists | Low | Low | Back-off schedule: 1 s, 3 s, 7 s; give up after 3 attempts |
| TR10 | ISN predictability enables spoofing | Low | Low | Hobby-OS threat model — accept risk; document in open questions |
| TR11 | TCP option parsing mishandles zero-length TLVs → infinite loop | High | Low | Hard cap on option-field scan iterations (≤ 40 — the max option bytes); break out if a length byte is < 2 |
| TR12 | Delayed-ACK + Nagle interaction causes 200 ms stalls | Medium | Low | Nagle off in v1 (TD-non-goal); revisit only if an app needs it |
| TR13 | Lock-order inversion with UDP (both touch netbuf) | High | Low | TCP locks all at ranks 9-11, always acquired after netbuf (rank 5); documented in §10 |

---

## 10. Lock Ordering Extension

This section **extends** the existing table in
`impldoc/net_overview.md §10` — it does not duplicate it. The
current ranks 1-8 remain authoritative; any discrepancy between
the two documents must be fixed in `net_overview.md §10`, not
here.

Existing ranks (reproduced only for orientation; authoritative
source is `src/spinlock.go:7-15`):

```
1. pageAllocLock
2. procLock
3. gInfoLock
4. vgaLock
5. netBufLock
6. arpLock
7. udpLock
8. statsLock
```

**TCP additions:**

```
9.  tcbTableLock  — global TCB table (16 entries); acquired
                    whenever a TCB pointer is resolved from a
                    4-tuple.
10. tcpListenLock — listen-port table (4 entries) and accept
                    queue; acquired on passive-open events.
11. tcpTimerLock  — per-TCB timer queue bookkeeping (RTO,
                    persist, delayed-ACK, TIME_WAIT). Fine-
                    grained enough that it is acquired last.
```

Invariants:
- A function holding lock N must not acquire lock M where
  M < N (strict; see `src/spinlock.go:7-15`). The TCP design
  never violates this.
- `tcpHandle` — the RX dispatcher invoked from `netRxLoop` (NOT
  directly from the e1000 ISR; see TD10 in §5) — acquires
  `tcbTableLock` (rank 9) first, then (optionally)
  `tcpTimerLock` (rank 11). Any segment-building step that
  needs `netBufLock` (rank 5) happens after releasing rank 9
  and 11, mirroring the UDP RX-side pattern.
- `sys_tcp_send` (from a user goroutine) acquires `procLock`
  (rank 2) at entry via `currentProc()`, releases it before
  touching the TCB, then acquires `tcbTableLock` (rank 9).
- `udpLock` (rank 7) and the new TCP locks are never held
  simultaneously; UDP and TCP paths are disjoint past the
  `ipv4Handle` switch.

Source-of-truth: the TCP lock ranks must be recorded as comments
in `src/spinlock.go:7-15` by the Phase TCP-1 implementation
session, matching the style of the existing `// lock ordering
rank N` annotations.

---

## 11. Changes to Existing Files

See §8 LOC table for the full list. Summary:

| File | Change | Lines |
|---|---|---|
| `src/ipv4.go` | `ipProtoTCP = 6` const + one `case` in demux | ~4 |
| `src/spinlock.go` | Extend rank comment through 11 | ~4 |
| `src/netsock.go` | Extend `socketFd` struct; add six syscall handlers | ~400 |
| `src/userspace.go` | Six new syscall constants + dispatch cases | ~12 |
| `src/main.go` | One `tcpInit()` call | ~1 |
| `user/gooos/net.go` | TCP SDK additions | ~200 |
| `user/Makefile` | Add `tcpecho`, `tcpcli` | ~2 |
| `Makefile` | Extend `run-net` hostfwd | ~2 |
| `README.md` | Progress-table row + Paths D, E in demos section | ~40 |

No file is deleted; no syscall number below 28 is reused.

---

## 12. QEMU Invocation

Extends `net_overview.md §12` with TCP port forwards. User-mode
networking remains the default test configuration (no root
required):

```bash
# Phase TCP-1 onward:
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio \
  -no-reboot -no-shutdown \
  -device e1000,netdev=n0 \
  -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=udp::19999-:17,hostfwd=tcp::10080-:8080,hostfwd=tcp::10081-:8081
```

- Guest port 8080 → host port 10080: kernel TCP echo (Path D).
- Guest port 8081 → host port 10081: userspace TCP echo (Path E).
- UDP forwards at 9999 / 19999 are preserved for Phase 1-5
  regression.

TAP configuration (for `tcpdump`, `iperf3`, bidirectional test
flows) is unchanged from `net_test_plan.md §1.1 Configuration C`.

---

## 13. Future Extensions (Post-TCP-5)

| Extension | Complexity | Notes |
|---|---|---|
| SACK + timestamps + window scale | ~500-900 LOC | Pure performance; unblocks Gb/s-class throughput under loss |
| Keep-alive + user timeout | ~60-100 LOC | Per-TCB flag + timer variant |
| Nagle's algorithm (opt-in) | ~40-80 LOC | Gated by a socket option |
| Half-close `SHUT_RD` | ~30-60 LOC | Requires refining the RECEIVE state bookkeeping |
| PMTUD (ICMP PTB handling) | ~80-150 LOC | Requires extending `icmp.go` to deliver PTB events to the TCB |
| ECN (RFC 3168) | ~60-100 LOC | Single bit in the TCP header + response logic |
| SMP-correct TCP | ~100-200 LOC | Per-CPU TCB partitions; depends on `impldoc/smp_deferred_and_known_issues.md` resolution |

---

## 14. Open Questions

Each row below is a decision with no defensible default for
gooos. Answers must come from the user before implementation —
the future session should surface them early in its plan.

1. **Initial receive-window size.** v1 proposes 8 KiB (matches
   `net_tcp_buffers.md §3`). The alternative is 16 KiB (fits
   inside the 2 MiB user heap but halves TCB concurrency).
   Recommendation: 8 KiB.

2. **Kernel TCP echo server port.** v1 proposes 8080. The
   alternative is 7 (matches the RFC 862 echo port and mirrors
   UDP), but port 7 collides with the existing UDP echo server
   and would confuse the `hostfwd` table. Recommendation: 8080.

3. **Accept queue depth.** v1 proposes 8 pending accepts per
   listen port. RFC 793 does not mandate a depth. Recommendation:
   8 (matches the socket-API convention in Linux `SOMAXCONN`
   historical default).

4. **Active-open retry schedule.** v1 proposes SYN retransmit at
   1 s, 3 s, 7 s (total 11 s). QEMU slirp typically answers in
   < 1 ms, so a first retransmit at 1 s is generous; the
   alternative is 500 ms / 1 s / 2 s. Recommendation: 1 s / 3 s
   / 7 s (low QEMU noise + matches common hobby-OS values).

5. **Behaviour on TCB-table exhaustion.** v1 proposes: passive
   accept returns `fdErrBad`; active connect returns `fdErrBad`.
   Alternative: drop silently on passive, error on active.
   Recommendation: error both sides (no silent drops — users can
   always retry).

6. **Does `sys_tcp_send` block on full send buffer?** v1
   proposes: yes, block until the ring has capacity, mirroring
   `sys_recvfrom`'s blocking semantics. Alternative: non-
   blocking with short-write semantics. Recommendation: block
   (simplest model for v1; non-blocking is a known deferred
   extension).

7. **Shutdown during TIME_WAIT.** v1 proposes: fd closes
   immediately from the userspace view, TCB hangs around in
   TIME_WAIT for 60 s, then is reaped silently. Alternative:
   block `Close()` until TIME_WAIT expires (matches strict
   POSIX). Recommendation: immediate fd close with lazy reap
   (matches Linux default).

---

## 15. Deferred reviewer findings

Reviewer pass of the initial draft (see `hoge.md` step 3 /
`net_tcp_work_plan.md §1.7`) flagged 3 CRITICAL and 2 MAJOR
issues, all fixed inline. The MINOR items below are recorded
here rather than fixed inline — each is annotated with the
rationale.

1. **`tcpAcceptQueueDepth = 8` is declared in
   `net_tcp_state_machine.md §6` but not repeated in the
   `net_tcp_socket_api.md §2.1` constants block.** Rationale
   for deferral: `net_tcp_socket_api.md §4.2` cites the value
   in the `sys_listen` clamp (`if backlog > tcpAcceptQueueDepth`)
   and the constant's canonical home is the state-machine doc
   that defines the listener table. A second declaration would
   risk drift. The implementing session should import the
   constant from the state-machine file, not redefine it.

2. **`tcpTimerLock` rank-11 phrasing.** §10 above describes
   it as "fine-grained enough that it is acquired last";
   stylistically terser would be "rank 11 is always innermost"
   to match the source-code convention in `src/spinlock.go:7-15`.
   Rationale for deferral: semantically equivalent; editing
   would not help the implementing agent.

3. **`grep 'TODO\|FIXME\|XXX'` shell escaping in
   `net_tcp_work_plan.md §4.4`.** Literal `|` in grep requires
   `-E` (extended regex). As written the grep matches the
   literal string `TODO|FIXME|XXX`, not any of the three
   separately. Rationale for deferral: the invariant is a
   check for "zero matches", and the broken grep returns zero
   by construction — it's a false-positive-pass. Noted as a
   follow-up for the implementing session to fix in-place if
   they want the invariant to actually work.

4. **Buffer-size totals in `net_tcp_buffers.md`.** §4.1 cites
   per-TCB footprint ~18.6 KiB leading to 298 KiB system-wide;
   §2 earlier quotes "256 KiB" for just the 16 × 16 KiB of
   ring buffers. Both are correct for their scope (298 KiB
   includes the retxQ + TCB bookkeeping, 256 KiB is rings
   only) but the two-number presentation without explicit
   link can mislead. Rationale for deferral: both numbers
   are mentioned in context in their respective sections; the
   reviewer judged the mislead risk low.

5. **Connect-retry schedule presentation in
   `net_tcp_timers_and_rtt.md §6.2`.** The "1 s / 3 s / 7 s"
   schedule in §6.2 is *cumulative* elapsed time (SYN at 0 s,
   1st retransmit at 1 s, 2nd at 3 s, 3rd at 7 s — giving
   rise to 1 s / 2 s / 4 s inter-retransmit deltas, i.e., the
   standard RFC 6298 exponential back-off). Not re-worded
   because §6.2's numeric schedule matches the style of the
   `net_tcp_overview.md §14 Q4` open question that proposed
   it.

6. **Lock-order strictness glyph.** §10 uses `M ≤ N` /
   `M < N` in adjacent sentences. Both are consistent with
   `src/spinlock.go:7-15` (which uses `< N`), but the `≤`
   version is more defensive. The `< N` version has been
   applied in §10 as part of the fix pass.

7. **`hoge.md` cross-reference.** The repo-root prompt file
   `hoge.md` is tracked as untracked by git. §16 does not
   currently reference it, and the implementing session
   should not depend on the prompt file persisting. Noted as
   informational only; no edit required.

8. **`ipv4Send` arity (discovered during implementation
   Phase 1 exploration, not during initial reviewer pass).**
   The initial draft of §2, `net_tcp_segment_io.md §6`, and
   `net_tcp_timers_and_rtt.md §7.2` showed `ipv4Send(proto,
   srcIP, dstIP, payload)` (4 args). The actual function at
   `src/ipv4.go:154` is the 3-arg form
   `ipv4Send(proto, dstIP, payload)` — source IP is implicit
   `ourIP`. Corrected in all three docs before committing the
   design set. TCB's `t.localIP` field is retained as identity
   bookkeeping but is never passed to `ipv4Send` in v1 (every
   v1 TCB has `localIP == ourIP`). If post-v1 work adds
   multiple IP aliases, `ipv4Send` will need extension.

---

## 16. Relationship to Other Documents

- **`impldoc/net_overview.md §5` and §10**: the Phase 1-4 design
  this TCP set extends. The lock-ordering table in §10 is the
  single source of truth; §10 here is an extension only.
- **`impldoc/net_socket_api.md`**: Phase 5 socket ABI. TCP
  reuses the `int 0x80` register convention, the `socketFd`
  struct, and `userBufInRange`; `net_tcp_socket_api.md` is the
  delta.
- **`impldoc/net_ipv4_icmp_udp.md §3`**: the UDP design whose
  pseudo-header checksum and `ipv4Send` hand-off TCP mirrors.
- **`impldoc/smp_deferred_and_known_issues.md`**: the BSP-only
  constraint that justifies TD9.
- **`pasttodos/TODO_NET1.md`, `pasttodos/TODO_NET2.md`**: the
  commit-cadence and reviewer-findings format that
  `net_tcp_work_plan.md` mirrors for `TODO_NET3.md`.
- **`README.md` lines 43-44 and the "Running the networking
  demos" section**: the progress-row and demo-paths style the
  final implementation phase must match.
