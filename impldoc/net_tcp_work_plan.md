# Networking Stack — TCP Work Plan

Phase-by-phase execution plan for implementing the TCP design
set. A future Claude Code session in plan mode will use this
document verbatim as its checklist.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Per-component designs:
[`net_tcp_state_machine.md`](net_tcp_state_machine.md),
[`net_tcp_segment_io.md`](net_tcp_segment_io.md),
[`net_tcp_timers_and_rtt.md`](net_tcp_timers_and_rtt.md),
[`net_tcp_flow_and_congestion.md`](net_tcp_flow_and_congestion.md),
[`net_tcp_socket_api.md`](net_tcp_socket_api.md),
[`net_tcp_buffers.md`](net_tcp_buffers.md),
[`net_tcp_test_plan.md`](net_tcp_test_plan.md).

---

## 1. Session Workflow

> **Audience note.** This document is read by the *future
> implementation session* that turns the TCP design into
> code. Every rule below applies to that session. The
> design-doc authoring session that produced this document
> makes **no** commits and does **not** create
> `TODO_NET3.md` itself.

1. **Plan mode first.** On session start, produce a plan that
   references this document and surface any Open Questions
   from the doc set that still need user answers. Use
   `ExitPlanMode` to request approval.
2. **`TODO_NET3.md` on plan approval.** Create
   `/home/ryo/work/gooos/pasttodos/TODO_NET3.md` (matching the
   location convention of `TODO_NET1.md` and `TODO_NET2.md`)
   and populate it with every checkbox from §2 below. Mark
   `- [x]` when each commit lands and its verification passes.
3. **Commit cadence.** One git commit per `TODO_NET3.md`
   checkbox. Match the `scope(subsys): ...` subject style from
   `git log`.
4. **No push, no master merge** unless the user explicitly
   says so (from `CLAUDE.md`: "Don't merge to master/main
   without order from the user.").
5. **Subagents liberally**: spawn `Explore` for code probes
   and `general-purpose` for parallel implementation of
   independent files. Avoid duplicating a subagent's search.
6. **Verification before done**: no box is ticked until its
   listed verification passes (`make build` clean, serial log
   match, etc.).
7. **Reviewer pass at the end.** After Phase TCP-5 is
   functionally complete, spawn a `general-purpose` reviewer
   subagent with the CRITICAL/MAJOR/MINOR punch-list brief (see
   `net_tcp_overview.md §15` for the deferred-MINOR mechanism).
   Fix CRITICAL + MAJOR inline; record MINOR at the tail of
   `net_tcp_overview.md` under "Deferred reviewer findings".
8. **Final completeness check**: grep the diff for new
   `TODO` / `FIXME` / `XXX` markers; each `- [x]` maps to a
   commit; `make lint` / `make build` clean.

---

## 2. Phase Checklist

### Phase TCP-1 — Passive open + kernel echo server

Goal: Host `nc 127.0.0.1 10080` completes handshake against a
kernel TCP echo server on guest port 8080, round-trips a
payload, and cleanly closes.

- [ ] `feat(net): ipProtoTCP constant + ipv4Handle case` — add
      `ipProtoTCP = uint8(6)` to `src/ipv4.go:20` constants
      and insert `case ipProtoTCP: tcpHandle(hdr, inner)` in
      the demux at `src/ipv4.go:184-213`. Verify: `make build`
      clean; T1.2 (RST on unhandled TCP) per
      [`net_tcp_test_plan.md §2`](net_tcp_test_plan.md).
- [ ] `feat(net): tcp_segment.go parse/build + checksum` —
      implement `tcpParse` / `tcpBuildSegment` /
      `tcpParseOptions` / `tcpBuildMSSOption` / `tcpChecksum` /
      `tcpChecksumVerify` / `tcpComputeAndSetChecksum` per
      [`net_tcp_segment_io.md §3-§4`](net_tcp_segment_io.md).
      Verify: `make build` clean; T_segment unit-tests in
      `tmp/` parse a known pcap segment byte-for-byte.
- [ ] `feat(net): TCB + tcbTable + tcb{Alloc,Free,Lookup}` —
      new `src/tcp.go` with `TCB` struct fields per
      [`net_tcp_state_machine.md §2`](net_tcp_state_machine.md),
      the 16-entry table, and `tcbTableLock` (rank 9). Add
      rank 9 comment to `src/spinlock.go:7-15`. Verify:
      `make lint` + `make build` clean.
- [ ] `feat(net): tcpRingBuf + rbWrite/rbRead/rbPeek` —
      implement per
      [`net_tcp_buffers.md §3`](net_tcp_buffers.md) inside
      `src/tcp.go`. Verify: unit-test wrap + full + empty
      invariants.
- [ ] `feat(net): tcp state machine dispatch + LISTEN path` —
      implement the `tcpStateClosed` / `tcpStateListen` /
      `tcpStateSynReceived` / `tcpStateEstablished` /
      `tcpStateCloseWait` / `tcpStateLastAck` branches from
      [`net_tcp_state_machine.md §3.2`](net_tcp_state_machine.md).
      Include listener table + accept queue per §6-§7.
      Acquire `tcpListenLock` as rank 10 (add to
      `src/spinlock.go`). Verify: `make build` clean;
      T1.3 LISTEN creation.
- [ ] `feat(net): ISN generator + tcpSendSegment` — implement
      `isnNext` ([§5](net_tcp_state_machine.md)) and the
      shared send path per
      [`net_tcp_segment_io.md §6`](net_tcp_segment_io.md).
      Lock-order: release rank 9 before calling `ipv4Send`.
      Verify: T1.4 3-way handshake in pcap.
- [ ] `feat(net): tcpRejectSegment + RST-on-no-match` —
      implement the unacceptable-segment path (RFC 793 §3.9)
      and RST generation for the "no TCB, no listener" case
      from [`net_tcp_segment_io.md §7`](net_tcp_segment_io.md).
      Verify: T1.2 + T1.7.
- [ ] `feat(net): kernel tcpEchoServer goroutine on port 8080`
      — spawn from `netInit` (or a new `tcpInit`) alongside
      the existing UDP echo. Bind via the listener table,
      accept in a goroutine, echo bytes back. Verify: T1.5
      small echo round-trip.
- [ ] `feat(net): netDiag TCP rows` — extend `netDiag()` in
      `src/net.go` (current lines 126-179) to dump per-TCB
      state and listener table. Verify: T1.3 serial output.
- [ ] `chore(net): Makefile run-net hostfwd tcp::10080-:8080`
      — add the hostfwd per
      [`net_tcp_test_plan.md §1.1`](net_tcp_test_plan.md).
      Verify: `make run-net` starts clean.
- [ ] `test(net): tmp/test_tcp_phase1.sh` — automate T1.1-T1.8
      (excluding the TCB-exhaustion test which needs external
      tooling). Verify: exit 0.
- [ ] `test(net): TCB exhaustion + accept-queue overflow` —
      manual verification via `hping3` SYN flood or a
      scripted Python scapy attack. Record result in
      TODO_NET3 tail.

### Phase TCP-2 — Active open + retransmission + RTT

Goal: Guest `tcpcli.elf` reaches a host-resident server; lost
segments retransmit with RTO back-off; RTT estimator
converges; full FIN close via TIME_WAIT.

- [ ] `feat(net): SYN_SENT path + tcpActiveConnect` —
      implement the active-open branch of
      [`net_tcp_state_machine.md §3.2`](net_tcp_state_machine.md).
      Include connect-timer goroutine per
      [`net_tcp_timers_and_rtt.md §6.2`](net_tcp_timers_and_rtt.md).
      Verify: T2.2 (timeout path).
- [ ] `feat(net): tcp_retx.go retransmission queue + RTO` —
      implement `tcpRetxQueue` per
      [`net_tcp_segment_io.md §5`](net_tcp_segment_io.md) and
      the RTO goroutine per
      [`net_tcp_timers_and_rtt.md §3`](net_tcp_timers_and_rtt.md).
      Add rank 11 (`tcpTimerLock`) to `src/spinlock.go`.
      Verify: T2.3 data retransmission under forced loss.
- [ ] `feat(net): tcp_rtt.go SRTT/RTTVAR/RTO (RFC 6298)` —
      implement `tcpRTTInit` / `tcpRTTUpdate` / `clampRTO`
      per [`net_tcp_timers_and_rtt.md §2`](net_tcp_timers_and_rtt.md).
      Include Karn's rule in the retxAckTo consumer. Verify:
      T2.4 + T2.5.
- [ ] `feat(net): FIN path (FIN_WAIT_1/FIN_WAIT_2/CLOSING)` —
      complete the remaining
      [state-machine](net_tcp_state_machine.md) branches.
      Verify: T1.6 and T2.6.
- [ ] `feat(net): TIME_WAIT timer` — implement
      `timeWaitDeadline` scheduling and the fire path per
      [`net_tcp_timers_and_rtt.md §5`](net_tcp_timers_and_rtt.md).
      Verify: T2.6 (TIME_WAIT → CLOSED after 60 s) and T2.7
      (re-ACK on retransmitted FIN).
- [ ] `test(net): tmp/test_tcp_phase2.sh` — automate T2.1-T2.7
      (requires TAP mode — gate on `sudo` / capability check).
      Verify: exit 0 on both user-mode (for T2.2) and TAP
      (for T2.1-T2.7).

### Phase TCP-3 — Flow control

Goal: Receive-window bookkeeping is tight and honest; zero-
window probes recover stalled connections; delayed ACKs fire
correctly.

- [ ] `feat(net): tcp_flow.go — rcv window + SWS avoidance` —
      implement `tcpAdvertiseWin` +
      `lastAdvWin` field per
      [`net_tcp_flow_and_congestion.md §2 + §5.1`](net_tcp_flow_and_congestion.md).
      Verify: T3.1 + T3.3 + T3.4.
- [ ] `feat(net): snd window update (RFC 793 §3.9)` —
      implement the `sndWl1`/`sndWl2` guard from
      [§3.2](net_tcp_flow_and_congestion.md).
      Verify: small unit test — window grows on a fresh ACK,
      stays unchanged on a stale duplicate.
- [ ] `feat(net): persist timer` — implement zero-window
      probing per
      [`net_tcp_timers_and_rtt.md §6.1`](net_tcp_timers_and_rtt.md)
      and [`net_tcp_flow_and_congestion.md §4`](net_tcp_flow_and_congestion.md).
      Verify: T3.2.
- [ ] `feat(net): delayed-ACK timer` — implement
      `delackDeadline` scheduling + "every other segment"
      acceleration per
      [`net_tcp_timers_and_rtt.md §4`](net_tcp_timers_and_rtt.md).
      Verify: T3.5 + T3.6.
- [ ] `test(net): tmp/test_tcp_phase3.sh` — automate
      T3.1-T3.6. Verify: exit 0.

### Phase TCP-4 — Congestion control (RFC 5681)

Goal: Slow start + congestion avoidance + fast retransmit
recover from loss without collapse; cwnd/ssthresh track
correctly.

- [ ] `feat(net): tcp_cc.go — iw() + slow start + CA` —
      implement per
      [`net_tcp_flow_and_congestion.md §6.2-§6.4`](net_tcp_flow_and_congestion.md).
      Add `cwndAccum` field to TCB. Verify: T4.1 + T4.2.
- [ ] `feat(net): fast retransmit + fast recovery` —
      implement `dupAcks` counter, 3-dup-ACK trigger, and the
      `cwnd = ssthresh + 3*mss` logic per
      [§6.5](net_tcp_flow_and_congestion.md).
      Verify: T4.3 + T4.4.
- [ ] `feat(net): RTO → cwnd collapse` — wire
      [`net_tcp_flow_and_congestion.md §6.6`](net_tcp_flow_and_congestion.md)
      into the RTO fire path from
      [`net_tcp_timers_and_rtt.md §3.2`](net_tcp_timers_and_rtt.md)
      step 5. Verify: T4.5.
- [ ] `test(net): tmp/test_tcp_phase4.sh` — automate T4.1-T4.5
      (T4.6 iperf3 is optional post-v1). Verify: exit 0.

### Phase TCP-5 — Socket API + Ring-3 SDK + demos + README

Goal: Ring-3 programs create and use TCP streams via an
ergonomic SDK; a userspace echo server exists; README
progress table reflects the new capability.

- [ ] `feat(net): socketFd kind discriminant + sockKind* consts`
      — extend `socketFd` at `src/netsock.go:90-94` per
      [`net_tcp_socket_api.md §3`](net_tcp_socket_api.md).
      Every existing Phase 5 call site must observe zero
      semantic change for UDP. Verify: `make build` + full
      Phase 5 regression (TODO_NET2 Part C).
- [ ] `feat(net): sys_socket branch for SOCK_STREAM` — extend
      `sysSocketHandler` at `src/netsock.go:138-155` per
      [§4.0](net_tcp_socket_api.md). Verify: `make build`
      clean.
- [ ] `feat(net): sys_bind TCP branch + tcpReservePort /
      tcpEphemeralPort` — extend `sysBindHandler` at
      `src/netsock.go:158-184` per
      [§4.1 + §6](net_tcp_socket_api.md). Verify: small
      unit test — TCP port 7 and UDP port 7 can coexist.
- [ ] `feat(net): sys_listen handler` — new handler per
      [§4.2](net_tcp_socket_api.md). Add dispatch case in
      `src/userspace.go`. Verify: T5.1.
- [ ] `feat(net): sys_accept handler + tcpAcceptWait` —
      per [§4.3](net_tcp_socket_api.md). Verify: T5.1 + T5.2.
- [ ] `feat(net): sys_connect handler + tcpActiveConnect
      Ring-3 entry` — per [§4.4](net_tcp_socket_api.md).
      Verify: T5.3.
- [ ] `feat(net): sys_tcp_send handler + tcpWriteFromUser`
      — per [§4.5](net_tcp_socket_api.md). Short-write
      semantics. Verify: T5.2 end-to-end; manual test of
      `TCPSendAll` loop.
- [ ] `feat(net): sys_tcp_recv handler + tcpReadIntoUser`
      — per [§4.6](net_tcp_socket_api.md). Timeout via
      `R10` matches `sys_recvfrom`. Verify: T5.4.
- [ ] `feat(net): sys_shutdown handler + tcpShutdownWrite /
      tcpShutdownBoth` — per [§4.7](net_tcp_socket_api.md).
      Verify: T5.5.
- [ ] `feat(net): userspace.go syscalls 28-33 dispatch` —
      per [§5](net_tcp_socket_api.md). Verify: `make build`
      + `make lint` clean.
- [ ] `feat(net): user/gooos/net.go TCP SDK` — `TCPSocket` /
      `TCPListen` / `TCPAccept` / `TCPConnect` / `TCPSend` /
      `TCPSendAll` / `TCPRecv` / `TCPShutdown` per
      [§7](net_tcp_socket_api.md). Verify: `make -C user`
      clean.
- [ ] `test(net): user/cmd/tcpecho/main.go` — minimal
      userspace echo server. Accept loop, per-connection
      `TCPRecv` → `TCPSendAll`, close on EOF. Bind port 8081
      so kernel echo on 8080 continues uninterrupted.
- [ ] `test(net): user/cmd/tcpcli/main.go` — minimal
      userspace client. Takes `ip port message` as argv,
      connects, sends, reads response, prints to stdout.
- [ ] `feat(net): embed tcpecho.elf + tcpcli.elf in kernel`
      — update `user/Makefile` CMDS and the `main.go`
      `fsCreate/fsWrite` block (pattern from TODO_NET2 item
      "embed udpecho.elf + dhcp.elf in kernel"). Verify:
      shell `ls` shows both ELFs; `make build` clean.
- [ ] `feat(net): Makefile run-net hostfwd tcp::10081-:8081`
      — add the userspace hostfwd. Verify: `make run-net`
      starts clean.
- [ ] `test(net): Phase TCP-5 end-to-end verification under
      QEMU` — interactively run T5.1-T5.6; run
      `tmp/test_tcp_phase5.sh` harness; confirm Phase 1-5
      regression (T5.7).
- [ ] `chore(net): reviewer pass (CRITICAL+MAJOR) + fix` —
      spawn a `general-purpose` reviewer subagent with the
      brief from [`net_tcp_overview.md §15`](net_tcp_overview.md)
      and the content-rules checklist. Fix CRITICAL + MAJOR
      inline; record MINOR findings at the tail of
      `net_tcp_overview.md` under "Deferred reviewer
      findings". Verify: reviewer agrees no CRITICAL or
      MAJOR remain.
- [ ] `docs(README): TCP milestone row + demo Paths D + E`
      — **Mandatory as the final phase step.** Update
      `README.md`:
      1. Add a new progress-table row after line 44
         (matching the style of the "Socket API + DHCP
         client" row — bolded lead phrase, backticked file
         paths, semicolon-separated feature list, final
         pointer to `impldoc/net_tcp_overview.md` and
         `pasttodos/TODO_NET3.md`).
      2. Extend the "Running the networking demos" section
         (currently README.md lines 46-187):
         - Add rows for **Path D** (kernel TCP echo,
           hostfwd 10080→8080) and **Path E** (userspace
           TCP echo, hostfwd 10081→8081) to the summary
           table (currently lines 52-55).
         - Extend the ASCII flow diagram (currently lines
           59-85) to depict TCP alongside UDP.
         - Add per-path subsections for D and E with
           example `nc` / `curl` invocations and expected
           output.
         - Update the lock-ordering note at the tail of the
           ASCII diagram (currently line 88-90) to include
           ranks 9-11 consulted on the TCP path.
- [ ] `docs(readme): pasttodos/TODO_NET3.md finalisation` —
      ensure every checkbox above is `- [x]` with a
      corresponding commit; add "Deferred further (not in
      this TODO)" and "Reviewer findings" tails matching
      `pasttodos/TODO_NET2.md` style.

---

## 3. Files Created and Modified

Summary of the post-Phase-TCP-5 diff, for the final
completeness check:

**New files:**

- `src/tcp.go` — TCB, state machine, listener + accept queue,
  ring buffers, kernel echo goroutine.
- `src/tcp_segment.go` — header parse/build + checksum +
  options + retxQ primitives.
- `src/tcp_retx.go` — RTO/persist/delack/TIME_WAIT/connect
  timer goroutines.
- `src/tcp_rtt.go` — SRTT/RTTVAR/RTO math.
- `src/tcp_flow.go` — rcv/snd window bookkeeping + SWS.
- `src/tcp_cc.go` — slow start + CA + fast retransmit +
  fast recovery.
- `user/cmd/tcpecho/main.go` — userspace echo server.
- `user/cmd/tcpcli/main.go` — userspace client.
- `pasttodos/TODO_NET3.md` — the executed checklist.
- `tmp/test_tcp_phase{1,2,3,4,5}.sh`,
  `tmp/test_tcp_regression.sh` — automation.

**Edited files:**

- `src/ipv4.go` — `ipProtoTCP = 6` + `case ipProtoTCP:`.
- `src/netsock.go` — `socketFd` extension + 6 new syscall
  handlers + 2 new helpers (`tcpRead` / `tcpWrite`).
- `src/userspace.go` — syscall numbers 28-33 + dispatch cases.
- `src/spinlock.go` — rank-ordering comment extended to 11.
- `src/main.go` — one call to `tcpInit()` after `netInit()`.
- `src/net.go` — `netDiag` extended with TCP rows.
- `Makefile` — `run-net` hostfwd additions.
- `user/gooos/net.go` — TCP SDK block.
- `user/Makefile` — `tcpecho`, `tcpcli` in `CMDS`.
- `README.md` — progress row + demo Paths D + E.
- `impldoc/net_tcp_overview.md` — "Deferred reviewer
  findings" tail populated by the reviewer pass.

**Not modified:**

- `src/udp.go`, `src/arp.go`, `src/ethernet.go`, `src/e1000.go`,
  `src/netbuf.go`, `src/netstats.go`, `src/pci.go`,
  `src/afterticks.go`, `src/stubs.S`.
- `user/rt0.S` — no new syscall stub required.

---

## 4. Cross-cutting Invariants (verify after every phase)

1. `make lint` passes — no new ISR-unsafe allocations.
2. `make build` clean — no stale symbols.
3. `make verify-globals` passes — no new top-level state
   outside `_globals_start.._globals_end`.
4. `grep -rn 'TODO\|FIXME\|XXX' src/tcp*.go src/netsock.go`
   yields zero hits at phase end.
5. Existing `scripts/test_net.sh` (Phase 1-4 harness) passes
   unchanged.
6. Existing `dhcp.elf` still completes DORA (Phase 5
   regression).
7. The six new TCP syscalls (28-33) appear in the canonical
   table at `impldoc/shell_io_fd_table.md §5.1` — add them as
   part of the Phase TCP-5 `docs(readme)` commit.

---

## 5. Reviewer Brief (for the Phase TCP-5 subagent)

When launching the reviewer subagent, pass this brief:

> **Goal**: audit the gooos TCP implementation against its
> design docs. Verify the design set is implementation-
> realised, not bypassed.
>
> **Files to read**:
> - All `impldoc/net_tcp_*.md`.
> - All new `src/tcp*.go` and edits to `src/netsock.go`,
>   `src/ipv4.go`, `src/userspace.go`, `src/spinlock.go`,
>   `src/main.go`, `src/net.go`.
> - `user/gooos/net.go` TCP additions.
> - `user/cmd/tcpecho/main.go`, `user/cmd/tcpcli/main.go`.
> - `README.md` progress row + demos section.
>
> **Audit checks**:
> - Lock-order ranks 9-11 are declared in `src/spinlock.go`
>   and respected in every acquire/release pair.
> - The `case ipProtoTCP:` demux hook is present.
> - ISR-reachable code paths pass `make lint`.
> - Every state-machine transition in
>   `net_tcp_state_machine.md §3.2` has a corresponding
>   code branch.
> - RFC 6298 constants match (`alpha=1/8`, `beta=1/4`,
>   `K=4`, `G=1 tick`, RTO min 1 s, RTO max 60 s).
> - `userBufInRange` gates every user pointer in the 6
>   new syscalls.
> - `socketFd.kind` is checked before every TCP operation.
> - README row and demo paths reflect the shipped code.
>
> **Output**: punch list — not a narrative. Classify each
> finding as CRITICAL / MAJOR / MINOR. Expect 0-5 MAJOR and
> 5-15 MINOR based on the Phase 5 precedent.

---

## 6. Stop Conditions

Pause and ask the user when:

- A design doc contradicts itself or another doc on a point
  the implementation depends on.
- A code branch would violate the lock-order rules in
  `net_tcp_overview.md §10`.
- QEMU triple-faults under TCP traffic — surface the serial
  log, don't paper over with retries.
- Phase 5 regression (UDP echo, DHCP DORA) breaks — revert
  the offending commit and ask.
- The reviewer pass flags an unresolvable CRITICAL (requires
  a design change, not a code change).

---

## 7. Relationship to Other Documents

- **`net_tcp_overview.md`**: the source of phase scope, design
  decisions, risk register, and open questions that gate
  Phase TCP-5.
- **`net_tcp_state_machine.md`** / `net_tcp_segment_io.md` /
  `net_tcp_timers_and_rtt.md` / `net_tcp_flow_and_congestion.md`
  / `net_tcp_socket_api.md` / `net_tcp_buffers.md`: per-
  component contracts this work plan realises.
- **`net_tcp_test_plan.md`**: verification gates cited from
  each checkbox in §2.
- **`pasttodos/TODO_NET1.md`**: commit-cadence + reviewer-
  findings structure.
- **`pasttodos/TODO_NET2.md`**: most recent precedent for a
  Phase-style `TODO_NETn.md` file and its "Deferred further"
  + "Reviewer findings" tails.
- **`README.md`**: the user-visible record the final
  `docs(README)` commit updates.
