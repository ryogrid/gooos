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
- [ ] `chore(net): reviewer pass (CRITICAL+MAJOR) + final completeness
      for Phase 5` — run reviewer subagent, fix CRITICAL+MAJOR, record
      MINOR below, confirm every checked box has a commit and no new
      TODO/FIXME/XXX in the diff.
- [ ] `docs(README): Phase 5 milestone row` — add a "Socket API + DHCP"
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

(Populated by the reviewer pass at end.)
