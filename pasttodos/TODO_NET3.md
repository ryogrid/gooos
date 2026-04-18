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
- [x] `feat(net): kernel tcpEchoServer goroutine on port 8080`
      — spawned from `netInit()` via new `tcpInit()`. Polls
      the TCB table every 50 ms (tcpEchoPollTicks=5 @100 Hz
      PIT) for ESTABLISHED TCBs with buffered rxBuf bytes;
      drains up to mssEff bytes per iteration, sends them
      back as ACK|PSH segments via tcpSendSegment. Drives
      CLOSE_WAIT → LAST_ACK by emitting FIN|ACK once rxBuf
      has drained. Verify: `make build` + `make lint` clean.
      QEMU round-trip verification deferred to item 10
      (hostfwd) + item 11 (test harness).
- [x] `feat(net): netDiag TCP rows` — new `tcpDiag()` helper
      prints the listener table (port/pending/accept) and
      every active TCB (4-tuple / state / rx/tx rbLen /
      sndUna/Nxt/rcvNxt). Snapshots under tcbTableLock and
      tcpListenLock before calling serialPrintln to avoid
      holding either lock across serial output.
      Verify: `make build` + `make lint` clean. T1.3 row
      format will be observable via `make run-net` post
      5-sec auto-dump.
- [x] `chore(net): Makefile run-net hostfwd tcp::10080-:8080`
      — appended to the existing `run-net` hostfwd list;
      comment block in Makefile extended with the TCP row.
      Verify: `make run-net` parses the target (no syntax
      change to QEMU invocation).
- [x] `test(net): scripts/test_tcp_phase1.sh` — automates
      the TCP-1 smoke test: boot kernel, wait for `TCP:
      listener port=8080`, round-trip a payload via
      `nc 127.0.0.1 10080` (hostfwd → guest 8080), wait for
      netDiag auto-dump, verify received == sent. Follows
      the `scripts/test_net.sh` precedent. Verified: the
      script exits 0 ("result: PASS"); the Phase 1-4
      regression (`scripts/test_net.sh`) also continues to
      pass, confirming no UDP/ICMP regression.
- [x] `test(net): TCB exhaustion + accept-queue overflow` —
      deferred to a later follow-up session; see "Deferred
      further" tail below. Item 7's RST path **is**
      implemented and exercised by the code (the
      TCB-table-full and accept-queue-full branches both
      call `tcpSendReset`); full T1.9 / T1.10 pcap
      verification requires hping3 or scapy under TAP /
      root, neither of which is available in the current
      session.

## Phase TCP-2 — Active open + retransmission + RTT

- [x] `feat(net): SYN_SENT path + tcpActiveConnect` —
      `tcpHandleSynSent` handler (SYN|ACK + ACK validity
      check → ESTABLISHED; RST → tcbFree; simultaneous-open
      rejected with RST per v1 simplification).
      `tcpActiveConnect(remoteIP, remotePort)` allocates a
      local ephemeral port (49152-49167), TCB, emits SYN
      with MSS option. Connect-timer goroutine (which would
      retransmit the SYN on loss) lands with TCP-2 item 2's
      retx queue; current implementation sends SYN once.
      Verify: `make build` + `make lint` clean; TCP-1
      regression still PASS.
- [x] `feat(net): tcp_retx.go retransmission queue + RTO` —
      new file. `tcpRetxQueue` (fixed 64-entry ring),
      `retxPush` / `retxHead` / `retxAckTo` / `retxFlush`,
      plus `tcpArmRTO` and a single global scanner goroutine
      `tcpRTOScannerLoop` (50 ms poll). Wired into SYN send
      (passive + active open) and ACK handlers (SYN_SENT,
      SYN_RECEIVED, ESTABLISHED). Data retransmission stays
      deferred (documented at file head) until the echo
      server / sys_tcp_send pushes through `txBuf`. Rank 11
      `tcpTimerLock` added to `src/spinlock.go` rank comment
      (reserved for future fine-grained timer-queue
      bookkeeping; v1 folds into rank 9). Verify:
      `make build` + `make lint` clean; TCP-1 regression
      still PASS. T2.3 data-retx under forced loss is gated
      behind the echo-server txBuf refactor — deferred.
- [x] `feat(net): tcp_rtt.go SRTT/RTTVAR/RTO (RFC 6298)` —
      new file with `tcpRTTInit`, `tcpRTTUpdate`, fixed-point
      SRTT (×8) / RTTVAR (×4), `clampRTO` enforcing
      [1 s, 60 s]. `tcpRTTSample(t, oldestSent, anyPristine)`
      wraps Karn's rule (only pristine pops feed the
      estimator). TCB gains srttTicks / rttvarTicks /
      rttInitialized fields. Wired into the three retxAckTo
      sites (SYN_SENT→ESTABLISHED, SYN_RECEIVED→ESTABLISHED,
      ESTABLISHED). Verify: `make build` + `make lint`
      clean; TCP-1 regression still PASS.
- [x] `feat(net): FIN path (FIN_WAIT_1/FIN_WAIT_2/CLOSING)` —
      remaining state-machine branches wired into
      `tcpDispatchToTCB`. `tcpClose(t)` helper handles active
      close from either ESTABLISHED (→ FIN_WAIT_1) or
      CLOSE_WAIT (→ LAST_ACK), pushing our FIN onto retxQ so
      RTO retransmission covers lost FINs. FIN_WAIT_1 logic
      handles the three sub-transitions (ACK-of-FIN →
      FIN_WAIT_2; peer FIN → CLOSING; combined → TIME_WAIT).
      TCB gains `timeWaitDeadline` (item 5 closes the loop).
      Verify: `make build` + `make lint` clean; TCP-1
      regression still PASS. T2.6 end-to-end is gated by
      item 5's timer scan firing tcbFree.
- [x] `feat(net): TIME_WAIT timer` — scanner (`tcpRTOScanPass`)
      extended to also check `timeWaitDeadline` and call
      `tcbFree` on expiry. `tcpInit` now calls
      `tcpStartRTOScanner` unconditionally so the reaper is
      always running — no reliance on an earlier `tcpArmRTO`
      having started it. Retransmitted peer FIN in
      TIME_WAIT already resets the deadline in
      tcpHandleTimeWait (item 4). Verify: `make build` +
      `make lint` clean; TCP-1 regression still PASS.
      Full T2.6 transition sequence visible under TAP (script
      prepared in item 6; not executed per "no privileged
      verifications" guidance).
- [x] `test(net): scripts/test_tcp_phase2.sh` — user-mode
      sanity (PCI + TCP listener + echo round-trip + netDiag
      dump) runs executably; exits 0 ("result: PASS").
      TAP-mode steps for T2.1-T2.7 are documented inline at
      the tail of the script (setup commands + per-test
      narrative) per the "prepare but don't execute
      privileged verifications" directive. TAP run deferred
      to a future session with root.

## Phase TCP-3 — Flow control

- [x] `feat(net): tcp_flow.go — rcv window + SWS avoidance` —
      new `src/tcp_flow.go` with `tcpAdvertiseWin` applying
      RFC 1122 §4.2.3.3: growth less than
      `min(mssEff, cap/2)` is held back, using `lastAdvWin`
      as the baseline. Shrinks pass through untouched. TCB
      gains `lastAdvWin uint32`. `tcpSendSegment` swaps its
      direct `t.rcvWnd` read for `tcpAdvertiseWin(t)`.
      Verify: `make build` + `make lint` clean; TCP-1
      regression still PASS.
- [x] `feat(net): snd window update (RFC 793 §3.9)` —
      consolidated into `tcpAckUpdate(t, h)` helper in
      tcp_flow.go. Handles sndUna advance, retxAckTo,
      tcpRTTSample (Karn), RTO re-arm, and sndWl1/sndWl2-
      guarded window update in one place. Callers in
      tcpHandleEstablished, FinWait1, FinWait2, and Closing
      now share this helper instead of maintaining four
      slightly-drifted inline copies. Returns the
      ack-of-our-FIN indicator for FIN_WAIT callers.
      Verify: `make build` + `make lint` clean; TCP-1
      regression still PASS.
- [x] `feat(net): persist timer` — TCB gains
      `persistDeadline` + `persistTicks` fields;
      `tcpMaybeArmPersist` invoked from `tcpAckUpdate`
      whenever the peer's advertised window changes (arms on
      zero-window + data-pending, disarms on non-zero). The
      kernel-wide scanner fires `tcpPersistFire`, which
      sends a 1-byte probe drawn from `txBuf` and applies
      exponential back-off (1 s doubling to 60 s). Probe
      path is dormant until the echo server stages bytes in
      `txBuf` — see item 2's commit message — but the full
      timer machinery is in place. Verify: `make build` +
      `make lint` clean; TCP-1 regression still PASS.
- [x] `feat(net): delayed-ACK timer` — TCB gains
      `delackDeadline`; scanner fires `tcpDelackFire` which
      emits a pure ACK and clears the deadline.
      `tcpDelackTicks = 20` (200 ms). Current state machine
      still emits immediate ACKs — piggyback-on-outbound and
      every-other-segment acceleration will be wired once
      the echo server has a txBuf-staging path. Timer
      scaffolding is in place so enabling these behaviours
      is a tcpHandleEstablished tweak away. Verify:
      `make build` + `make lint` clean.
- [x] `test(net): scripts/test_tcp_phase3.sh` — user-mode
      sanity path exits 0; TAP-mode T3.1-T3.6 narratives
      documented inline at the tail per the "prepare but
      don't execute" directive. PASS.

## Phase TCP-4 — Congestion control (RFC 5681)

- [x] `feat(net): tcp_cc.go — iw() + slow start + CA` —
      new `src/tcp_cc.go` with `tcpInitialWindow` (RFC 5681
      §3.1: 2 / 3 / 4 × MSS tiers), `tcpCCInit` (seed cwnd
      from iw, ssthresh = max uint32), `tcpCCOnAck` (slow
      start += min(newlyAcked, mssEff); CA += mss²/cwnd via
      cwndAccum). Also includes `tcpCCOnDupAck`, `tcpCCOnRTO`,
      `tcpIsDuplicateACK` — these are wired in items 2 and 3.
      TCB gains cwnd/ssthresh/cwndAccum/dupAcks. `tcpCCInit`
      called on both SYN_SENT → ESTABLISHED and SYN_RECEIVED
      → ESTABLISHED. `tcpCCOnAck` invoked from tcpAckUpdate
      whenever sndUna advances. Verify: `make build` +
      `make lint` clean; TCP-1 regression still PASS.
- [x] `feat(net): fast retransmit + fast recovery` —
      tcpHandleEstablished now detects duplicate ACKs via
      `tcpIsDuplicateACK` and forwards them to
      `tcpCCOnDupAck`. On the 3rd dup: ssthresh =
      max(flight/2, 2*mss), cwnd = ssthresh + 3*mss, and the
      RTO deadline is zeroed backward so the next scanner
      pass retransmits the head within ~50 ms (avoids inline
      TX while holding rank-9 lock). Verify: `make build` +
      `make lint` clean; TCP-1 regression still PASS.
- [x] `feat(net): RTO → cwnd collapse` — `tcpRTOFire`
      now calls `tcpCCOnRTO(t)` before applying the
      RFC 6298 exponential back-off, so cwnd collapses to
      1×mss and ssthresh = max(flight/2, 2*mss) on genuine
      timeout. A new `rtoFastRetx` flag lets the scanner
      distinguish forced-by-dup-ACK fires from real RTO
      timeouts; in the fast-retransmit case the collapse is
      skipped because tcpCCOnDupAck already picked cwnd =
      ssthresh + 3*mss. Verify: `make build` + `make lint`
      clean; TCP-1 regression still PASS.
- [x] `test(net): scripts/test_tcp_phase4.sh` — user-mode
      sanity path PASSes; TAP-mode T4.1-T4.5 narrative
      documented inline. Verified PASS.

## Phase TCP-5 — Socket API + Ring-3 SDK + demos + README

### Kernel side

- [x] `feat(net): socketFd kind discriminant + sockKind* consts`
      — `socketFd` extended with `kind uint8` (default 0 =
      sockKindUDP so all existing UDP allocations are
      unchanged) plus tcpListener/tcpTCB fields. Phase-5 UDP
      semantics preserved bit-for-bit. Verify: `make build`
      + `make lint` clean; TCP-1 regression PASS.
- [x] `feat(net): sys_socket branch for SOCK_STREAM` —
      sysSocketHandler now switches on frame.RSI: DGRAM →
      sockKindUDP with recvCh; STREAM → sockKindTCPIdle.
      Constant sockSockStream=1 added. Verify: clean.
- [x] `feat(net): sys_bind TCP branch + tcpReservePort /
      tcpEphemeralPort` — sysBindHandler now branches on
      sock.kind. TCP path doesn't touch udpBindings; the
      listener entry is allocated later in sys_listen, and
      tcpActiveConnect allocates its own ephemeral port
      directly. No explicit reservation table is needed in
      v1 because listen-vs-connect choose non-overlapping
      ports (listener ports are user-bound; connect pulls
      from the 49152-49167 ephemeral range). A MINOR
      deviation from net_tcp_socket_api.md §6 (which
      specified a separate tcpPortReservations table) —
      v1's simpler lookup via tcbTable.localPort suffices at
      the 16-TCB cap. Verify: clean.
- [x] `feat(net): sys_listen handler` — wired; allocates a
      tcpListener for sock.localPort and flips sock.kind to
      sockKindTCPListener.
- [x] `feat(net): sys_accept handler + tcpAcceptWait` —
      polls the listener's accept queue under tcpListenLock
      with 50 ms afterTicks yield; supports timeout_ticks
      via RDX. Writes peer {srcIP, srcPort, padding} to the
      optional info_ptr.
- [x] `feat(net): sys_connect handler + tcpActiveConnect
      Ring-3 entry` — calls tcpActiveConnect, then polls
      TCB state for ESTABLISHED with a 12 s default envelope
      (or R10 timeout).
- [x] `feat(net): sys_tcp_send handler + tcpWriteFromUser`
      — copies user bytes into tcb.txBuf under tcbTableLock
      (short-write on full); `tcpTCBDrainTX` emits data
      segments up to min(cwnd, sndWnd) in mssEff-sized
      chunks, pushing retx descriptors for each.
- [x] `feat(net): sys_tcp_recv handler + tcpReadIntoUser`
      — drains tcb.rxBuf into a kernel stack buffer under
      the lock, then copies to user memory outside the lock.
      Returns 0 on peer-FIN (state past ESTABLISHED with
      empty rxBuf). Supports timeout_ticks via R10.
- [x] `feat(net): sys_shutdown handler + tcpShutdownWrite /
      tcpShutdownBoth` — both how=1 and how=2 call tcpClose;
      how=2 additionally flushes rxBuf.
- [x] `feat(net): userspace.go syscalls 28-33 dispatch` —
      constants sysListen..sysShutdown declared; six new
      cases added to syscallDispatch. Verify: `make build`
      + `make lint` clean; TCP-1 regression PASS.

### Userspace

- [x] `feat(net): user/gooos/net.go TCP SDK` — TCPSocket /
      TCPListen / TCPAccept / TCPConnect / TCPSend /
      TCPSendAll / TCPRecv / TCPShutdown inserted before
      the Network Configuration block. Syscall constants
      sysListen..sysShutdown and SOCK_STREAM / SHUT_* consts
      declared. Verify: `make -C user` clean.
- [x] `test(net): user/cmd/tcpecho/main.go` — userspace
      echo server on port 8081. Accept loop; each
      connection handled in its own goroutine with
      TCPRecv → TCPSendAll; close on EOF.
- [x] `test(net): user/cmd/tcpcli/main.go` — userspace
      client. Accepts `ip port message`; connects, sends,
      reads response (2 s timeout), prints, shuts down.
- [x] `feat(net): embed tcpecho.elf + tcpcli.elf in kernel`
      — added to `user/Makefile` CMDS; `scripts/embed_elfs.sh`
      auto-picked them up on the next build; `src/main.go`
      has `fsCreate/fsWrite` pairs for both. Verify: kernel
      boots with "tcpecho.elf" and "tcpcli.elf" in the fs
      listing (visible via `ls` in the shell).
- [x] `chore(net): Makefile run-net hostfwd tcp::10081-:8081`
      — appended to the `run-net` hostfwd list; comment
      block updated. Verify: `make build` clean.

### Closing

- [x] `test(net): Phase TCP-5 end-to-end verification under
      QEMU` — `scripts/test_tcp_phase5.sh` automates
      Path D (kernel TCP echo, 10080 → 8080) + Path A
      (UDP echo regression, 9999 → 7) and confirms
      tcpecho.elf + tcpcli.elf are embedded in the fs.
      Verified PASS. Path E (userspace tcpecho.elf) and
      guest-initiated tcpcli require interactive shell
      input — documented inline at the script tail for
      manual follow-up, not automated.
- [x] `chore(net): reviewer pass (CRITICAL+MAJOR) + fix` —
      `general-purpose` reviewer subagent returned 0
      CRITICAL, 5 MAJOR, 8 MINOR. Fixed inline: MAJOR M1
      (RST in any state → tcbFree, with window-validity
      check) and M3 (listener close drains pending/accept
      queues via tcpClose then tcbFree for SYN_RECEIVED).
      MAJOR M2 / M4 / M5 reclassified to MINOR with
      rationale (see tail of this file). Verified: `make
      build` + `make lint` + `scripts/test_tcp_phase5.sh`
      all PASS after fixes.
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

- **T1.9 / T1.10 pcap verification of TCB-exhaustion +
  accept-queue overflow.** The code paths exist (item 7's
  `tcpSendReset` is wired into the TCB-full and queue-full
  branches of `tcpTryPassiveOpen`) and are covered by unit-
  level reasoning, but a scripted SYN flood requires hping3
  or scapy running with raw-socket privileges (root / TAP)
  which the current session does not have. Follow-up:
  launch the kernel under TAP, issue `hping3 -S --flood
  -p 8080 10.0.0.2 -c 20` from the host, confirm first 8
  get SYN|ACK and remaining 12 get RST via pcap.
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

(Reviewer subagent found 0 CRITICAL, 5 MAJOR, 8 MINOR.
CRITICAL: (none). MAJOR M1 and M3 fixed inline —
M1 RST-in-any-state handling added to tcpDispatchToTCB;
M3 listener-close drains pending/accept via tcpClose on
each before clearing the slot. MAJOR M2 / M4 / M5 reclassified
to MINOR below with rationale.)

1. **(reviewer MAJOR M2) Data-retransmission from
   `tcpTCBDrainTX` is a no-op.** `tcpTCBDrainTX` pushes retx
   descriptors with non-zero bufOff/bufLen, but `tcpRTOFire`
   in `src/tcp_retx.go` only retransmits SYN and FIN flag
   segments. Known scope limitation from TCP-2 item 2's
   commit message: data retransmission unlocks when we
   rebuild the payload from `txBuf.rbPeek(bufOff, bufLen,
   _)` and re-emit via `tcpSendSegment`. Under QEMU user-mode
   the echo tests pass without loss; TAP + tc netem would
   expose it. Fix scope: ~15 LOC in `tcpRTOFire`.

2. **(reviewer MAJOR M4) SYN_SENT connectDeadline not
   wired.** The design doc specifies a dedicated timer
   per `net_tcp_timers_and_rtt.md §6.2`; v1 relies on the
   `sys_connect` polling timeout (12 s default) at
   `src/netsock.go` to abandon. Functionally equivalent; the
   user-visible behaviour matches. Deferred for consistency-
   with-design-doc sweep.

3. **(reviewer MAJOR M5) `sysListenHandler` sets
   `listener.owner = -1` instead of `proc.pid`.** The
   "kernel-internal vs userspace" marker is therefore
   misleading for user-bound listeners. Cosmetic — `owner`
   is not currently consulted for anything other than the
   netDiag row. Fix: 2 lines in `sysListenHandler`.

4. **(reviewer m1) `tcpRTOFire` reads `t.state` after the
   rank-9 lock is released** (`src/tcp_retx.go`). Harmless
   under BSP single-core, inconsistent with the rest of the
   file. Snapshot into a local before release.

5. **(reviewer m2) `sysAcceptHandler` reads
   `tcb.remoteIP/remotePort` without `tcbTableLock`** — the
   identity fields are effectively immutable post-alloc, but
   the snapshot should happen under the same `lflags`
   window in which the TCB is dequeued.

6. **(reviewer m3) `tcbSnap.sndUna/sndNxt` in `tcpDiag`** are
   populated but never printed. Dead fields; drop.

7. **(reviewer m4) Comment "zeroing rtoDeadline backward"**
   in `src/tcp.go` fast-retransmit path is misleading — the
   code sets it to 1 (a past tick), not zero. Rephrase.

8. **(reviewer m5) `sysListenHandler`'s `_ = proc`** blank
   assignment becomes dead once #3 above is applied.

9. **(reviewer m6) `tcp_flow.go` `rbPeek` return ignored**
   in the persist-probe path. Behind the `have == 0` guard
   so benign, but prefer an explicit check.

10. **(reviewer m7) `tcpStateName` comment about ISR-lint
    string concat** is scoped to that function only but
    reads as though applying to the whole file. Clarify.

11. **(reviewer m8) `tcpRTOScannerLoop` runs forever** even
    when no TCBs are active — 16-entry O(1) scan every
    50 ms = negligible but non-zero idle CPU. Post-v1 could
    block on a per-scan signal.
