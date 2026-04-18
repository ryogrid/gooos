# TODO_NET3 — Networking Stack TCP Phases TCP-1 … TCP-5

Implementation of `impldoc/net_tcp_*.md` per `hoge.md`.
One commit per item (`scope(subsys): ...`); tick `- [x]` when
the commit lands and its listed verification passes.
Commit-message style follows `pasttodos/TODO_NET2.md` precedent.

## Phase TCP-1 — Passive open + kernel echo server

- [x] `feat(net): ipProtoTCP constant + ipv4Handle case` — add
      `ipProtoTCP = uint8(6)` near `src/ipv4.go:19` (alongside
      `ipProtoICMP` / `ipProtoUDP`) and insert `case
      ipProtoTCP: tcpHandle(hdr, inner)` in the demux switch
      at `src/ipv4.go:205-212`. Verify: `make build` clean;
      RST-on-no-match verification moved to item 7 below
      (this item only lands the const + switch case + a no-op
      `src/tcp.go` stub so the build stays green).
      Commit `1eceb97` — `make build` + `make lint` clean.
- [x] `feat(net): tcp_segment.go parse/build + checksum` —
      new `src/tcp_segment.go`. Exports `tcpParse`,
      `tcpBuildSegment`, `tcpParseOptions`,
      `tcpBuildMSSOption`, `tcpChecksum`,
      `tcpChecksumVerify`, `tcpComputeAndSetChecksum`.
      Verify: `make build` + `make lint` clean. Unit-level
      parse/build round-trip test deferred to the
      `test_tcp_phase1.sh` harness that exercises these
      functions end-to-end via real segments.
- [x] `feat(net): TCB + tcbTable + tcbAlloc/Free/Lookup` —
      new `src/tcp.go` with TCB struct per
      `net_tcp_state_machine.md §2`, 16-entry table, and
      `tcbTableLock` (rank 9). Extend the lock-ordering
      comment in `src/spinlock.go:7-15` to include rank 9.
      Verify: `make lint` + `make build` clean. Minimum-
      viable TCB fields landed; state-machine / buffer /
      timer / CC fields grow into this struct in subsequent
      TCP-1..TCP-4 items.
- [x] `feat(net): tcpRingBuf + rbWrite/rbRead/rbPeek` —
      byte-granular FIFO ring per `net_tcp_buffers.md §3`.
      Embedded in TCB as txBuf + rxBuf (8 KiB each). Methods:
      rbWrite / rbRead / rbPeek / rbFree / rbLen / rbCap /
      rbReset. Verify: `make build` + `make lint` +
      `verify-globals` clean. 16 TCB × 16 KiB buffers =
      256 KiB static .bss; well under kernel-heap budget.
- [x] `feat(net): TCP state machine dispatch + LISTEN path` —
      handlers for CLOSED, LISTEN, SYN_RECEIVED, ESTABLISHED,
      CLOSE_WAIT, LAST_ACK states (`net_tcp_state_machine.md
      §3.2`); listener table + accept queue (§6-§7). Adds
      `tcpListenLock` at rank 10 to `src/spinlock.go`;
      listener allocator + pending/accept queue helpers;
      `tcpTryPassiveOpen` wiring SYN → SYN_RECEIVED →
      SYN|ACK; ESTABLISHED handler copies payload into rxBuf
      and emits pure ACK; FIN → CLOSE_WAIT; LAST_ACK → ACK →
      free. Verify: `make build` + `make lint` clean. T1.3
      LISTEN-creation serial row is gated behind item 9
      netDiag work.
- [x] `feat(net): ISN generator + tcpSendSegment` —
      `isnNext()` (§5) and the shared send path
      (`net_tcp_segment_io.md §6`). Send path uses the 3-arg
      `ipv4Send(ipProtoTCP, t.remoteIP, seg)` form. Verify:
      `make build` + `make lint` clean. (Implemented before
      item 5 because item 5's state machine depends on this
      primitive — TODO order preserved for traceability but
      commit ordering respects the actual dependency.)
- [x] `feat(net): tcpRejectSegment + RST-on-no-match` —
      stateless RST helper `tcpSendReset` covering RFC 793
      §3.4 reply rules (seq=inAck when incoming ACK=1, else
      seq=0/ack=inSeq+segLen). Wired into tcpHandle (no TCB
      + no-SYN path), tcpTryPassiveOpen (no listener,
      listener-queue full, TCB-table full). Incoming RST is
      still dropped silently — never respond to RST with
      RST. Verify: `make build` + `make lint` clean. T1.2 +
      T1.7 pcap verification gated behind item 10 (hostfwd).
- [ ] `feat(net): kernel tcpEchoServer goroutine on port 8080`
      — spawned from `netInit()` analogous to
      `udpEchoServer` at `src/udp.go:312-324`. Verify: T1.5
      host `nc` round-trips a payload.
- [ ] `feat(net): netDiag TCP rows` — extend `netDiag()` in
      `src/net.go` to print per-TCB state + listener-table
      snapshot. Verify: T1.3 serial row format.
- [ ] `chore(net): Makefile run-net hostfwd tcp::10080-:8080`
      — append to the existing `run-net` hostfwd list.
      Verify: `make run-net` starts clean.
- [ ] `test(net): scripts/test_tcp_phase1.sh` — automate
      T1.1–T1.8 with serial-log grep; follows the
      `scripts/test_net.sh` precedent (not `tmp/`). Verify:
      exit 0.
- [ ] `test(net): TCB exhaustion + accept-queue overflow` —
      manual verification via scripted SYN flood (T1.9 +
      T1.10). Log result to TODO_NET3 tail if TAP unavailable.

## Phase TCP-2 — Active open + retransmission + RTT

- [ ] `feat(net): SYN_SENT path + tcpActiveConnect` —
      active-open branch of the state machine; connect-timer
      goroutine (`net_tcp_timers_and_rtt.md §6.2`). Verify:
      T2.2 (connect timeout retries then errors).
- [ ] `feat(net): tcp_retx.go retransmission queue + RTO` —
      new file. `tcpRetxQueue` per `net_tcp_segment_io.md §5`
      + RTO timer goroutine per
      `net_tcp_timers_and_rtt.md §3`. Add `tcpTimerLock`
      (rank 11) to `src/spinlock.go`. Verify: T2.3 data
      retransmit under forced loss.
- [ ] `feat(net): tcp_rtt.go SRTT/RTTVAR/RTO (RFC 6298)` —
      new file. `tcpRTTInit` / `tcpRTTUpdate` / `clampRTO`;
      Karn's rule in `retxAckTo`. Verify: T2.4 + T2.5.
- [ ] `feat(net): FIN path (FIN_WAIT_1/FIN_WAIT_2/CLOSING)` —
      remaining state-machine branches. Verify: T1.6 + T2.6.
- [ ] `feat(net): TIME_WAIT timer` — 60 s via `afterTicks`;
      re-ACK retransmitted FIN resets the deadline. Verify:
      T2.6 transition sequence + T2.7 re-ACK.
- [ ] `test(net): scripts/test_tcp_phase2.sh` — automate
      T2.1–T2.7 (T2.1/T2.3 require TAP; gate on capability
      and skip-with-note if unavailable). Verify: exit 0.

## Phase TCP-3 — Flow control

- [ ] `feat(net): tcp_flow.go — rcv window + SWS avoidance` —
      new file. `tcpAdvertiseWin` + `lastAdvWin` TCB field.
      Verify: T3.1 + T3.3 + T3.4.
- [ ] `feat(net): snd window update (RFC 793 §3.9)` —
      `sndWl1`/`sndWl2` guard. Verify: unit test — window
      grows on fresh ACK, unchanged on stale duplicate.
- [ ] `feat(net): persist timer` — zero-window probe per
      `net_tcp_timers_and_rtt.md §6.1` +
      `net_tcp_flow_and_congestion.md §4`. Verify: T3.2.
- [ ] `feat(net): delayed-ACK timer` — 200 ms +
      every-other-segment acceleration
      (`net_tcp_timers_and_rtt.md §4`). Verify: T3.5 + T3.6.
- [ ] `test(net): scripts/test_tcp_phase3.sh` — automate
      T3.1–T3.6. Verify: exit 0.

## Phase TCP-4 — Congestion control (RFC 5681)

- [ ] `feat(net): tcp_cc.go — iw() + slow start + CA` —
      new file. Verify: T4.1 + T4.2.
- [ ] `feat(net): fast retransmit + fast recovery` —
      `dupAcks` counter, 3-dup-ACK trigger,
      `cwnd = ssthresh + 3*mss`. Verify: T4.3 + T4.4.
- [ ] `feat(net): RTO → cwnd collapse` — wire into the RTO
      fire path from `net_tcp_timers_and_rtt.md §3.2`.
      Verify: T4.5.
- [ ] `test(net): scripts/test_tcp_phase4.sh` — automate
      T4.1–T4.5 (T4.6 iperf3 deferred per design doc).
      Verify: exit 0.

## Phase TCP-5 — Socket API + Ring-3 SDK + demos + README

### Kernel side

- [ ] `feat(net): socketFd kind discriminant + sockKind* consts`
      — extend `socketFd` at `src/netsock.go:90-94`
      per `net_tcp_socket_api.md §3`. Existing Phase-5 UDP
      paths observe zero semantic change. Verify:
      `make build` + TODO_NET2 Part C regression.
- [ ] `feat(net): sys_socket branch for SOCK_STREAM` —
      extend `sysSocketHandler` at
      `src/netsock.go:138-155` (§4.0). Verify:
      `make build` clean.
- [ ] `feat(net): sys_bind TCP branch + tcpReservePort /
      tcpEphemeralPort` — extend `sysBindHandler`
      (§4.1 + §6). Verify: unit test — TCP port 7 and UDP
      port 7 coexist.
- [ ] `feat(net): sys_listen handler` — new handler
      (§4.2) + dispatch in `src/userspace.go`. Verify: T5.1.
- [ ] `feat(net): sys_accept handler + tcpAcceptWait` —
      (§4.3). Verify: T5.1 + T5.2.
- [ ] `feat(net): sys_connect handler + tcpActiveConnect
      Ring-3 entry` — (§4.4). Verify: T5.3.
- [ ] `feat(net): sys_tcp_send handler + tcpWriteFromUser`
      — short-write semantics (§4.5). Verify: T5.2 +
      `TCPSendAll` loop behaviour.
- [ ] `feat(net): sys_tcp_recv handler + tcpReadIntoUser`
      — timeout via `R10` (§4.6). Verify: T5.4.
- [ ] `feat(net): sys_shutdown handler + tcpShutdownWrite /
      tcpShutdownBoth` — (§4.7). Verify: T5.5.
- [ ] `feat(net): userspace.go syscalls 28-33 dispatch` —
      constants + switch cases in `src/userspace.go:87-148`.
      Verify: `make build` + `make lint` clean.

### Userspace

- [ ] `feat(net): user/gooos/net.go TCP SDK` — TCPSocket /
      TCPListen / TCPAccept / TCPConnect / TCPSend /
      TCPSendAll / TCPRecv / TCPShutdown
      (`net_tcp_socket_api.md §7`). Inserted between the
      existing UDP block and config block. Verify:
      `make -C user` clean.
- [ ] `test(net): user/cmd/tcpecho/main.go` — userspace echo
      server on port 8081. Mirrors
      `user/cmd/udpecho/main.go`. Accept loop;
      per-connection TCPRecv → TCPSendAll; close on EOF.
- [ ] `test(net): user/cmd/tcpcli/main.go` — userspace
      client. `argv = ip port message`. Connect, send, read
      response, print to stdout.
- [ ] `feat(net): embed tcpecho.elf + tcpcli.elf in kernel`
      — add both to `user/Makefile` `CMDS` line; rerun
      `scripts/embed_elfs.sh`; add two `fsCreate`/`fsWrite`
      pairs after `src/main.go:482`. Verify: shell `ls`
      shows both ELFs; `make build` clean.
- [ ] `chore(net): Makefile run-net hostfwd tcp::10081-:8081`
      — append to the existing `run-net` hostfwd list.
      Verify: `make run-net` starts clean.

### Closing

- [ ] `test(net): Phase TCP-5 end-to-end verification under
      QEMU` — interactively run T5.1–T5.6 plus
      `scripts/test_tcp_phase5.sh`; confirm Phase 1-5
      regression (T5.7).
- [ ] `chore(net): reviewer pass (CRITICAL+MAJOR) + fix` —
      spawn `general-purpose` reviewer subagent per
      `net_tcp_work_plan.md §5`. Fix CRITICAL + MAJOR inline
      (as follow-up commits in this phase). Record MINOR at
      the tail of this file **and** at
      `impldoc/net_tcp_overview.md §15` under "Deferred
      reviewer findings" (preserving the existing 8
      initial-draft items). Verify: reviewer agrees no
      CRITICAL or MAJOR remain.
- [ ] `docs(README): TCP milestone row + demo Paths D + E`
      — update `README.md`:
      (a) new progress-table row after line 44 matching the
      "Socket API + DHCP client" row style.
      (b) extend the "Running the networking demos" section:
      Paths D (kernel TCP echo) and E (userspace
      `tcpecho.elf`) added to the summary table; ASCII flow
      diagram extended; lock-rank footnote updated to
      include ranks 9-11; per-path subsections with `nc` /
      `curl` invocation examples and expected output.
- [ ] `docs(net): TODO_NET3.md finalisation` — ensure every
      checkbox above is `- [x]` with a corresponding commit;
      populate "Deferred further" and "Reviewer findings"
      tails (below).

## Deferred further (not in this TODO)

- SACK (RFC 2018), TCP timestamps (RFC 7323), window scale.
- ECN (RFC 3168).
- Path MTU discovery (RFC 1191).
- Nagle's algorithm (off by default in v1 per design).
- Keep-alive timer.
- `shutdown(SHUT_RD)` half-close.
- TCP over IPv6.
- SMP correctness beyond BSP-only.
- TCP option state carried across connections.
- `iperf3` server port to gooos userspace (T4.6 gated).

## Reviewer findings

### CRITICAL

(populated during Phase TCP-5 reviewer pass)

### MAJOR

(populated during Phase TCP-5 reviewer pass)

### MINOR

(populated during Phase TCP-5 reviewer pass; copied in
parallel to `impldoc/net_tcp_overview.md §15`)
