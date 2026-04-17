# TODO_NET2 — Networking Stack Phase 5 (Socket API + DHCP Client)

Implementation of `impldoc/net_socket_api.md` + `impldoc/net_dhcp_client.md`,
deferred from the Phase 1–4 checklist in `TODO_NET1.md`. One commit per
item (`scope(subsys): ...`); tick `- [x]` when the commit lands and its
listed verification passes.

## Part A — Socket API (kernel + userspace SDK)

- [x] `feat(net): syscall5 stub for 5-argument syscalls` — add `syscall5`
      assembly wrapper in `user/rt0.S` and its Go declaration in
      `user/gooos/syscall.go`. Needed because `sys_sendto` takes five
      args (fd, buf, len, dstIP, dstPort). Verify: `make -C user` clean.
- [x] `feat(net): udpBindWithChannel + udpSendRaw (src/udp.go)` — add a
      bind variant that accepts an externally-owned channel (so
      `socketFd` can own its receive queue) and `udpSendRaw` that builds
      a frame with explicit src/dst IPs and a fixed destination MAC,
      bypassing ARP. Verify: `make build` clean.
- [x] `feat(net): ourDNS global + netDiag row` — `src/net.go` adds the
      DNS-server global alongside `ourIP`/`ourNetmask`/`ourGateway` and
      prints it in `netDiag`. Verify: `make run-net` shows `DNS:
      0.0.0.0` before any configuration.
- [x] `feat(net): socketFd + socket syscalls (src/netsock.go)` — new
      file with `socketFd` (`FileDesc` impl owning `recvCh`),
      `sys_socket`/`sys_bind`/`sys_sendto`/`sys_recvfrom`/
      `sys_net_config`/`sys_sendto_bcast` handlers. `sys_recvfrom`
      accepts `R8 = timeout_ticks` (0 = block forever) per the design
      doc's §12 open question. Verify: `make build` clean.
- [x] `feat(net): wire syscalls 22-27 in userspace.go` — add syscall
      number constants and dispatch `case` entries. Verify: `make build`
      clean.
- [x] `feat(net): userspace SDK user/gooos/net.go` — high-level
      `Socket`/`Bind`/`UDPSendTo`/`UDPRecvFrom`/`UDPRecvFromTimeout`/
      `UDPSendBroadcast` + `GetIP`/`SetIP`/`GetNetmask`/`SetNetmask`/
      `GetGateway`/`SetGateway`/`GetMAC`/`ApplyNetConfig`/`GetDNS`/
      `SetDNS` + `IPv4`/`FormatIP`/`FormatMAC` helpers. Verify:
      `make -C user` clean.
- [x] `test(net): user/cmd/udpecho — userspace UDP echo server` —
      proves the socket API end-to-end. Binds port 17 (keeping the
      kernel-builtin echo on port 7 intact for the existing Phase-1–4
      `test-net` harness); `run-net` adds a second hostfwd
      `udp::19999-:17`. Loops `UDPRecvFrom` → `UDPSendTo`. Embedded in
      kernel via `user_binaries.go`.

## Part B — DHCP Client (depends on Part A)

- [x] `feat(net): user/gooos/fs.go WriteFile helper` — wraps
      `sys_fs_write` for file-create-and-write. Used by DHCP client to
      record `/network.conf`. Verify: `make -C user` clean.
- [x] `feat(net): user/cmd/dhcp DHCP client` — full DORA exchange:
      generate XID, build + broadcast DHCPDISCOVER, recv DHCPOFFER,
      build + broadcast DHCPREQUEST, recv DHCPACK, apply via
      `sys_net_config`, write `/network.conf`, print summary. Uses
      4-second timeout on recvfrom per QEMU slirp expectation.
- [x] `feat(net): embed udpecho.elf + dhcp.elf in kernel` — add both to
      `user/Makefile` CMDS and the `main.go` fsCreate/fsWrite block so
      the shell sees them. Verify: `ls` in-shell shows `udpecho.elf`
      and `dhcp.elf`; `make build` clean.

## Part C — Verification + cross-cutting

- [x] `test(net): Phase 5 end-to-end verification under QEMU` — run
      `udpecho` from the shell while `test-net` hostfwd round-trips a
      payload through it; run `dhcp` and observe DORA in serial log,
      verify `/network.conf` contents with `cat network.conf`.
      **Verified** interactively under QEMU user-mode networking:
      (a) `phase5-udpecho-test` round-trips through userspace
      `udpecho.elf` via hostfwd 19999→17;
      (b) `dhcp` completes the full DORA against QEMU slirp's DHCP
      server, prints the 10.0.2.15 / 255.255.255.0 / gw 10.0.2.2 /
      DNS 10.0.2.3 / lease 86400s block; `cat network.conf`
      then shows the persisted file;
      (c) `netDiag` auto-dump reports `DNS: 10.0.2.3` after the
      lease is applied;
      (d) `scripts/test_net.sh` still PASSes (Phase 1-4 regression).
- [x] `chore(net): reviewer pass (CRITICAL+MAJOR) + final completeness
      for Phase 5` — run reviewer subagent, fix CRITICAL+MAJOR, record
      MINOR below, confirm every checked box has a commit and no new
      TODO/FIXME/XXX in the diff. **Done.** Reviewer: 0 CRITICAL, 3
      MAJOR (all fixed in a follow-up commit), 9 MINOR (recorded
      below).
- [x] `docs(README): Phase 5 milestone row` — add a "Socket API + DHCP"
      progress-table row referencing syscalls 22-27 and the
      `user/cmd/dhcp` program.

## Deferred further (not in this TODO)

- TCP / SOCK_STREAM, connect/listen/accept.
- select/poll/epoll multiplexing.
- Non-blocking sockets / O_NONBLOCK.
- Raw sockets (SOCK_RAW).
- IPv6 / AF_INET6.
- DNS resolver in the kernel or userspace.
- DHCP lease renewal / rebinding / RELEASE.
- Static IP fallback on DHCP failure.
- Multiple NIC support.

## Reviewer findings

### CRITICAL

None.

### MAJOR (all fixed)

1. **`socketFd` inheritance on spawn was unsafe** (`src/process.go` fd
   loop, `src/netsock.go:Close`) — shallow-copied sockets would share a
   single kernel bind-table entry; first child to exit would run
   `socketFd.Close → udpUnbind` and tear the port binding out from
   under the surviving process. Fix: the fd-inheritance loop now
   explicitly drops `*socketFd` slots (child gets `nil`), documented in
   the comment above the loop and in the `src/netsock.go` concurrency
   header.

2. **User pointer bounds check had no upper bound** (`src/netsock.go`,
   every syscall handler) — the previous check only rejected
   `bufPtr < 0x40000000`. A user passing a kernel-half pointer
   (≥ 0x8000000000000000) would cause the kernel copy loop to fault
   during the syscall. Fix: introduced `userAddrMax = 0x80000000` (2
   GiB, above the user stack at 0x7FFF2000 in `linker_user.ld`) and a
   `userBufInRange(ptr, length)` helper that validates `[ptr, ptr+length)`
   with overflow guard. All handlers (`sys_sendto`, `sys_recvfrom`
   bufPtr+infoPtr, `sys_sendto_bcast`, `netConfigGetMAC` ptr) now use
   the helper. Also replaced magic-number `1472` with
   `ipv4MaxPayload - udpHeaderSize` so MTU changes don't silently drift.

3. **`socketFd` fields mutated without synchronization** —
   `sock.bound` / `sock.localPort` / `sock.recvCh` are read/written
   across `sys_bind` / `sys_recvfrom` / `Close`. Under gooos's single-
   BSP cooperative scheduling this is safe today (handlers don't
   preempt each other and sockets are no longer cross-process shared
   per MAJOR#1). Fix: added a concurrency-assumptions block to the
   `src/netsock.go` header documenting the single-BSP contract and
   what would need to change (per-socket `Spinlock`) if true SMP
   preemption is ever enabled.

   Also added a `currentProc()==nil` guard to `sysNetConfigHandler`
   for contract symmetry with the other handlers (MINOR#4 from the
   reviewer report, folded into this commit).

### MINOR (recorded; not fixed in this milestone)

1. `src/netsock.go:afterTicks` timeout goroutine in `sys_recvfrom`
   keeps spinning (runtime.Gosched loop) until its deadline even when
   `<-sock.recvCh` won the select. Benign (self-terminates, no alloc);
   fix is a cancelable timer primitive, worth doing alongside any
   future `sys_sleep` / deadline-plumbing refactor.
2. `src/netsock.go:socketFd.Write` returns `fdErrBad` with no
   discriminator distinguishing "use sys_sendto instead" from a
   transient error. Documentation-only; userspace SDK already routes
   around it.
3. `user/cmd/dhcp:recvDHCP` cannot distinguish `UDPRecvFromTimeout`
   returning 0 (timeout) from "server sent garbage we couldn't parse".
   Both map to "no valid OFFER/ACK received". Improve by adding a
   ticked return value in the SDK or a separate sentinel.
4. `user/cmd/dhcp:recvDHCP` XID check runs AFTER the msgType/NAK
   check. A NAK with a wrong XID would print "server returned NAK"
   and bail. Reorder to XID-first for principled rejection.
5. Sock constants `sockAFInet` / `sockSockDgram` (kernel
   `src/netsock.go`) and `AF_INET` / `SOCK_DGRAM` (SDK
   `user/gooos/net.go`) are duplicated without a compile-time drift
   check. Values match today; a future divergence would be silent.
6. No scripted Phase-5 regression (`scripts/test_net.sh` covers
   Phase 1–4 only). Phase 5 was hand-verified under QEMU. An
   automated harness — boot QEMU, `sendkey udpecho\\n`, round-trip,
   `sendkey dhcp\\n`, grep `"dhcp: network configured"` — would
   lock this in.
7. `sys_sendto` / `sys_sendto_bcast` unsafe copy loops use
   `bufPtr + i` without wraparound check. Today `bufLen ≤ 1472` +
   userAddrMax bound make wraparound impossible; flagged for future
   audit if the cap changes.
8. `src/netsock.go:socketFd.Close` drains `recvCh` with a
   `select{case<-recvCh:default:}` loop that spins until empty. After
   a 16-element burst this is cheap; still, `for len(ch) > 0` would
   be clearer if TinyGo supports it on buffered channels (needs
   verification).
9. DHCP client never records the `secs` field of the BOOTP header
   (offset 8-9). Some pedantic servers care; QEMU slirp doesn't.

All MINOR items are documented for follow-up; none block Phase 5.
