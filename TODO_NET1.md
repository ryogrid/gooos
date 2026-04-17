# TODO_NET1 — Networking Stack Phases 1–4

Implementation of `impldoc/net_overview.md` Phases 1–4 per `hoge.md`.
One commit per item (`scope(subsys): ...`). Mark `- [x]` when the commit
lands and the listed verification passes.

## Phase 1 — e1000 NIC driver

- [x] `feat(net): add outl/inl 32-bit port I/O stubs` — extend `src/stubs.S`
      with `outl`/`inl` mirroring `outb`/`inb`. Verify: `make build` clean.
- [x] `feat(net): PCI bus scan and e1000 discovery` — `src/pci.go` with
      config read/write via 0xCF8/0xCFC, bus 0..31.0..7 scan, decode BAR0,
      enable bus master (Command reg bit 2), capture IRQ Line. Verify:
      `make build` clean.
- [x] `feat(net): e1000 driver init, descriptor rings, TX/RX` —
      `src/e1000.go`. Register constants, MMIO helpers (via `mapPage` with
      PCD+PWT above 1 GiB), reset, config, RX ring (64 descriptors) + RX
      buffers (32 pages via `allocPagesContig`), TX ring (32 descriptors) +
      TX buffers (16 pages), MAC read from RAL0/RAH0, link-up poll,
      `e1000Transmit`, `e1000TryReceive`. Descriptors are raw `[16]byte`
      with manual field accessors. Verify: `make build` clean.
- [x] `feat(net): e1000 IRQ handler` — `src/e1000_irq.go`. `//go:nosplit`
      `handleE1000IRQ` (ICR read-to-clear, signal `rxSignalCh` via
      non-blocking select, EOI via `picSendEOI` or `lapicSendEOI`).
      Verify: `make build` clean.
- [x] `feat(net): wire PCI+e1000 init into main.go` — insert
      `pciInit`/`e1000Init` after `lapicTimerInit` and before
      `go fsTask()`. Register `handleE1000IRQ` at vector `32 + IRQLine`.
      Verify: boot via `make run-net`, serial shows `PCI: found e1000`,
      `e1000: MAC=...`, `e1000: link up`.
- [x] `feat(net): Makefile run-net target` — add `run-net` (ISO +
      `-device e1000,netdev=n0 -netdev user,id=n0,hostfwd=udp::9999-:7`).
      Verify: `make run-net` brings up the VM with NIC attached. **Phase 1
      end-to-end verified**: serial log shows `PCI: found e1000 at 0:3.0
      BAR0=0xFEB80000 IRQ=11`, `e1000: MAC=52:54:00:12:34:56`, `e1000:
      link up`, `e1000: IRQ handler registered at vector 43`, `e1000: NIC
      initialized`; shell boots cleanly; no regressions.

## Phase 2 — Ethernet + ARP

- [x] `feat(net): byte-order and address format helpers` —
      `src/netutil.go` (htons/ntohs/htonl/ntohl, macToString, ipToString,
      parseIPv4). Verify: `make build` clean.
- [x] `feat(net): Ethernet framing and EtherType dispatch` —
      `src/ethernet.go` (frame parse/build, `etherTypeIPv4`=0x0800,
      `etherTypeARP`=0x0806, broadcastMAC, isForUs). Dispatch lives in
      net.go to avoid cross-file forward references to arpHandle /
      ipv4Handle. Verify: `make build` clean.
- [x] `feat(net): ARP cache, resolve, gratuitous` — `src/arp.go` (16-entry
      LRU cache under `arpLock` rank 6, parse/build, `arpResolve` with
      2 s timeout via `afterTicks(200)`, `arpSendGratuitous`, `arpHandle`).
      Verify: `make build` clean.
- [x] `feat(net): netInit, RX dispatch loop, static IP config` —
      `src/net.go` (ourIP/ourNetmask/ourGateway globals, `netInit`,
      `netRxLoop` polling version, `nextHopIP`). Verify: `make run-net`
      shows `ARP: sent gratuitous`; `NET: initialized`. **Phase 2
      end-to-end verified**: serial log contains `NET: initialized
      IP=10.0.2.15 gw=10.0.2.2`, `ARP: sent gratuitous announcement for
      10.0.2.15`, `NET: RX dispatch goroutine started`.

## Phase 3 — IPv4 + ICMP + UDP

- [x] `feat(net): IPv4 parse, build, checksum, dispatch` — `src/ipv4.go`
      (header parse/build, ones-complement checksum with odd-length
      zero-pad, `ipv4Send`, `ipv4Handle` protocol dispatch, drop on bad
      version/IHL/checksum/fragment/TTL=0; `ipv4ID` counter). Verify:
      `make build` clean.
- [x] `feat(net): ICMP echo reply + kernel self-test` — `src/icmp.go`
      (`icmpHandle` flips type to 0, recomputes checksum, sends via
      `ipv4Send`). In-kernel self-test feeds synthetic echo-request into
      `ipv4Handle` and verifies reply is transmitted (prints
      `TEST: icmp echo reply PASS`). **Verified**: serial log shows
      `TEST: icmp echo reply PASS` on boot.
- [x] `feat(net): UDP parse, checksum, bind table, echo server` —
      `src/udp.go` (pseudo-header checksum, `udpChecksumVerify`, 8-entry
      bind table under `udpLock` rank 7, `udpBind`/`udpUnbind`/`udpSend`,
      `udpEchoServer` goroutine on port 7). Wire `go udpEchoServer()` in
      main.go after `netInit`. Verify: `echo hi | nc -u 127.0.0.1 9999`
      echoes via hostfwd. **Phase 3 end-to-end verified**: host-side
      `echo -n hello-gooos | nc -u -w2 127.0.0.1 9999` → received
      `hello-gooos` back (full Ethernet + IP + UDP round-trip through
      QEMU slirp hostfwd 9999 → guest 7).

## Phase 4 — Robustness, buffers, diagnostics

- [x] `feat(net): packet buffer pool (128×2048)` — `src/netbuf.go`
      (allocPagesContig(64), [2]uint64 free bitmap, `ctz64`, `netBufAlloc`/
      `netBufFreeIdx`/`netBufSlice`, `netBufLock` rank 5). Verify:
      `make build` clean. **Verified**: serial log shows `TEST: netbuf
      lifecycle PASS` (fills pool, rejects 129th alloc, reclaims freed
      slot).
- [x] `feat(net): network statistics` — `src/netstats.go` (18-counter
      NetStats, `statsInc`, `netStatsSnapshot`, `statsLock` rank 8). Wire
      counters into ethernet/arp/ipv4/icmp/udp dispatch. Verify:
      `make build` clean.
- [x] `feat(net): interrupt-driven RX` — replace poll loop in
      `e1000.go`/`net.go` with `rxSignalCh`-driven `netRxLoop` that
      drains descriptor ring on each signal. Verify: `make run-net` under
      UDP echo traffic; packet counts rise without busy-looping.
      **Verified**: UDP echo round-trip (`echo -n irq-test-123 | nc -u -w2
      127.0.0.1 9999`) succeeds with the rxSignalCh-driven loop.
- [x] `feat(net): netDiag + boot-time auto-dump` — `netDiag()` in
      `net.go` prints link / MAC / IP / ARP cache / all counters; goroutine
      in main.go calls `<-afterTicks(500); netDiag()` after `netInit`.
      Verify: serial log contains `=== Network Diagnostics ===` block
      ~5 s after boot. **Verified**: block shows Link: UP, MAC, IP, GW,
      ARP cache entries, and all 18 counters.

## Cross-cutting

- [x] `chore(spinlock): document net lock ordering ranks 5-8` — extend
      comment header in `src/spinlock.go` (5 netBufLock, 6 arpLock,
      7 udpLock, 8 statsLock). Verify: `make build` clean.
- [x] `test(net): user-mode smoke test script` — `scripts/test_net.sh`
      boots QEMU with `run-net` to serial file, greps markers (PCI, MAC,
      link up, NET init, ARP gratuitous, UDP listening, netDiag),
      performs a `nc -u 127.0.0.1 9999` round-trip, exits 0 on all-pass.
      Add `test-net` Makefile target. **Verified**: `result: PASS` with
      all 9 markers + echo match.
- [x] `test(net): TAP integration test script` —
      `scripts/test_net_tap.sh` sets up `tap0`, runs QEMU with
      `-netdev tap`, asserts `ping -c 5` and `nc -u 7`, tears down. Add
      `test-net-tap` Makefile target. Not part of per-phase gate;
      available for users with root / CAP_NET_ADMIN. Script notes that
      the kernel IP is currently hard-coded to 10.0.2.15, so TAP runs
      require either `GOOOS_NET_TAP_TARGET` override or a subnet match;
      runtime IP reconfiguration is deferred to Phase 5 DHCP.
- [ ] `docs(README): networking milestone row` — add row to progress
      table after SMP reflecting e1000 + Ethernet/ARP/IPv4/ICMP/UDP
      completion.
- [ ] `chore(net): reviewer pass (CRITICAL+MAJOR) + final completeness` —
      run reviewer subagent, fix CRITICAL+MAJOR findings, record MINOR
      below, confirm every checked box has a commit and no new
      TODO/FIXME/XXX markers in the diff.

## Deferred to Phase 5 (not in this TODO)

- Socket syscalls 22–27 (`sys_socket`, `sys_bind`, `sys_sendto`,
  `sys_recvfrom`, `sys_net_config`, `sys_sendto_bcast`) and userspace
  `gooos/net` SDK. See `impldoc/net_socket_api.md`.
- Userspace DHCP client (`user/cmd/dhcp`) and `/etc/network.conf`. See
  `impldoc/net_dhcp_client.md`.

## Deferred further (future work)

- TCP, virtio-net, IPv6, IPv4 fragmentation/reassembly, ICMP Time
  Exceeded, IOAPIC routing for NIC IRQ.

## Reviewer MINOR findings

(Populated by the reviewer pass at end.)
