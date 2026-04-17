# Networking Stack — Overview and Work Plan

This document is the entry point to the design set for bringing
UDP/IP networking to gooos, a bare-metal x86-64 hobby OS written
in Go (TinyGo). Six documents under `impldoc/net_*.md` together
provide a complete implementation blueprint.

## 1. Problem Statement

gooos currently has no networking support. The kernel runs on
QEMU and has serial/VGA output, keyboard input, an in-memory
filesystem, goroutine-based scheduling, SMP support, and a
Ring-3 userspace with a shell — but no way to communicate over
a network.

### 1.1 Goals

1. **NIC driver** for Intel e1000 (82540EM), the default emulated
   NIC in QEMU (`-device e1000`).
2. **Ethernet framing** (L2) — parse and build 802.3 frames.
3. **ARP** — resolve IPv4 addresses to MAC addresses; respond to
   ARP requests from the host.
4. **IPv4** (L3) — send and receive IPv4 datagrams (no
   fragmentation, no options).
5. **ICMP echo** — respond to ping requests (`ping <kernel-ip>`).
6. **UDP** (L4) — send and receive UDP datagrams with a simple
   bind/listen API.
7. **Diagnostics** — counters, buffer pool, and a `netstat`
   shell command or probe ELF.

### 1.2 Non-Goals (Deferred)

- TCP (SYN/ACK/FIN state machine).
- DHCP client.
- DNS resolver.
- virtio-net driver.
- IPv6 / NDP.
- Socket syscall API for userland (`sys_socket`, `sys_sendto`).
- TLS.

## 2. Existing Infrastructure Leveraged

| Subsystem | File(s) | Reuse |
|---|---|---|
| I/O port access (`outb`/`inb`) | `src/stubs.S`, `src/serial.go` | Direct reuse; add `outl`/`inl` for 32-bit PCI config-space access |
| MMIO mapping (`mapPage`) | `src/vm.go` | Map e1000 BAR0 register space (identity-mapped with PCD+PWT) |
| Contiguous page allocation (`allocPagesContig`) | `src/vm.go` | DMA descriptor rings + packet buffers |
| Single-page allocation (`allocPage`/`freePage`) | `src/vm.go` | Intermediate page tables for MMIO mapping |
| Interrupt dispatch (`registerHandler`) | `src/interrupt.go` | e1000 IRQ handler registration |
| IOAPIC redirection (`ioapicSetRedirection`) | `src/ioapic.go` | Route NIC IRQ to BSP (if IOAPIC is re-enabled) |
| PIC pass-through | `src/pic.go`, `src/main.go:389` | Fallback: e1000 IRQ via legacy PIC |
| Serial logging (`serialPrint*`) | `src/serial.go` | Debug output throughout |
| Spinlock (`Spinlock`) | `src/spinlock.go` | Protect shared state (ARP cache, UDP bind table, stats) |
| Per-CPU storage (`cpuID()`) | `src/percpu.go` | ISR context; per-CPU TX/RX state if needed |
| Goroutines + channels | TinyGo scheduler | Async RX dispatch goroutine |
| `afterTicks(n)` | `src/afterticks.go` | ARP timeout; link-up wait |
| `hextoa` / `utoa` | `src/vm.go`, `src/main.go` | Debug formatting |
| Build toolchain | `Makefile`, `target.json` | No changes needed — `$(wildcard $(SRC_DIR)/*.go)` auto-discovers new files |

## 3. Document Coverage Table

| Doc | Scope | Home Blockers |
|---|---|---|
| `net_overview.md` (this file) | Entry point, phasing, DAG, risk register | — |
| `net_pci_e1000_driver.md` | PCI bus scan, e1000 register map, TX/RX ring, IRQ | B1–B5 |
| `net_ethernet_arp.md` | Ethernet frame parse/build, ARP cache, ARP req/reply | B6–B8 |
| `net_ipv4_icmp_udp.md` | IPv4, ICMP echo reply, UDP send/recv, bind table | B9–B12 |
| `net_buffers_diagnostics.md` | Buffer pool, statistics counters, shell diagnostics | B13–B15 |
| `net_test_plan.md` | Verification matrix, QEMU test harnesses, stress tests | — |

## 4. Blocker Inventory

| ID | Blocker | Resolved in |
|---|---|---|
| B1 | No PCI bus enumeration | `net_pci_e1000_driver.md §1` |
| B2 | No 32-bit I/O port access (`outl`/`inl`) | `net_pci_e1000_driver.md §1.2` |
| B3 | No e1000 register abstraction | `net_pci_e1000_driver.md §2` |
| B4 | No TX/RX descriptor rings | `net_pci_e1000_driver.md §3` |
| B5 | No NIC IRQ handler | `net_pci_e1000_driver.md §5` |
| B6 | No Ethernet frame parser | `net_ethernet_arp.md §1` |
| B7 | No byte-order utilities (`htons`/`ntohs`) | `net_ethernet_arp.md §3` |
| B8 | No ARP cache or resolver | `net_ethernet_arp.md §2` |
| B9 | No IPv4 parser/builder | `net_ipv4_icmp_udp.md §1` |
| B10 | No IPv4 checksum | `net_ipv4_icmp_udp.md §1.3` |
| B11 | No ICMP echo reply | `net_ipv4_icmp_udp.md §2` |
| B12 | No UDP send/recv | `net_ipv4_icmp_udp.md §3` |
| B13 | No packet buffer pool | `net_buffers_diagnostics.md §1` |
| B14 | No network statistics | `net_buffers_diagnostics.md §2` |
| B15 | No network diagnostic command | `net_buffers_diagnostics.md §3` |

## 5. Phased Work Plan

### Phase 1 — e1000 NIC Driver

**Goal:** Detect e1000 via PCI, map MMIO registers, bring link
up, transmit and receive raw Ethernet frames.

- [ ] **1a. 32-bit I/O port stubs** — Add `outl`/`inl` to
  `src/stubs.S` for PCI config-space mechanism 1.
- [ ] **1b. PCI bus enumeration** — `src/pci.go`: scan bus 0
  devices 0-31 functions 0-7; match vendor 0x8086 device
  0x100E; decode BAR0; enable bus-master.
- [ ] **1c. e1000 register constants** — `src/e1000_regs.go`:
  CTRL, STATUS, RCTL, TCTL, RDBAL/H, TDBAL/H, RDH, RDT,
  TDH, TDT, ICR, IMS, IMC, RAL, RAH, MTA, etc.
- [ ] **1d. e1000 init + TX/RX rings** — `src/e1000.go`:
  reset, MAC address read, descriptor ring setup (32 TX +
  64 RX), link-up wait, RCTL/TCTL configuration.
- [ ] **1e. e1000 transmit** — `e1000Transmit(frame []byte)`:
  copy to TX buffer, set descriptor, advance TDT.
- [ ] **1f. e1000 receive (polling)** — poll RDH/RDT; copy
  packet out when DD set; reset descriptor; advance RDT.
- [ ] **1g. e1000 IRQ handler** — `src/e1000_irq.go`: read
  ICR, signal RX goroutine via channel, send EOI.
- [ ] **1h. Boot integration** — Add `pciInit()` +
  `e1000Init()` calls in `src/main.go`.

**Verification:** QEMU `-device e1000`, read STATUS register,
TX broadcast frame captured via pcap dump.

### Phase 2 — Ethernet + ARP

**Goal:** Parse/build Ethernet frames, maintain ARP cache,
respond to ARP requests.

- [ ] **2a. Byte-order utilities** — `src/netutil.go`:
  `htons`/`ntohs`/`htonl`/`ntohl`, `macToString`,
  `ipToString`.
- [ ] **2b. Ethernet framing** — `src/ethernet.go`:
  `EthernetHeader`, `ethernetParse`, `ethernetBuild`,
  dispatch table by EtherType.
- [ ] **2c. ARP** — `src/arp.go`: parse/build ARP packets,
  fixed-size cache (16 entries), request/reply logic,
  `arpResolve(ip)` with timeout.
- [ ] **2d. Gratuitous ARP** — Send on init to pre-populate
  host ARP cache.
- [ ] **2e. RX dispatch goroutine** — `src/net.go`: receive
  loop that calls `ethernetParse` → ARP or IPv4 handler.

**Verification:** Host `ping` triggers ARP exchange visible
in pcap; ARP cache logged to serial.

### Phase 3 — IPv4 + ICMP + UDP

**Goal:** Send/receive IPv4 datagrams, respond to ping, provide
UDP socket-like API.

- [ ] **3a. IPv4** — `src/ipv4.go`: header parse/build,
  checksum, dispatch by protocol (ICMP=1, UDP=17).
- [ ] **3b. ICMP echo** — `src/icmp.go`: parse type/code,
  echo reply (swap src/dst, type=0, recompute checksum).
- [ ] **3c. UDP** — `src/udp.go`: header parse/build,
  pseudo-header checksum, bind table (8 entries), channel
  per port, `udpSend`/`udpBind`.
- [ ] **3d. Static IP config** — Global vars for IP, netmask,
  gateway; set during `netInit()`.
- [ ] **3e. Boot integration** — Add `netInit()` call in
  `src/main.go` after e1000 init.

**Verification:** `ping <kernel-ip>` from host succeeds;
UDP echo server on port 7 tested with `nc -u`.

### Phase 4 — Robustness and Diagnostics

**Goal:** Interrupt-driven RX, buffer pool, statistics, shell
diagnostic command.

- [ ] **4a. Interrupt-driven RX** — Replace polling with
  interrupt-driven receive; drain all ready descriptors
  on each IRQ.
- [ ] **4b. Buffer pool** — `src/netbuf.go`: fixed array of
  128 × 1536-byte buffers with free bitmap.
- [ ] **4c. Statistics counters** — `src/netstats.go`:
  TX/RX packets/bytes, drops, ARP hits/misses, checksum
  errors.
- [ ] **4d. Diagnostic output** — `netstat` command or ELF
  that prints link status, IP/MAC config, ARP cache,
  counters.
- [ ] **4e. Error handling** — Drop runt frames (<60 B),
  oversize (>1518 B), unknown EtherType, bad checksums,
  TTL=0.

**Verification:** Statistics counters non-zero after traffic;
buffer pool operates without leak.

## 6. Dependency DAG

```
Phase 1 (e1000 driver):
  [1a] outl/inl stubs
       │
       └──► [1b] PCI bus scan
                  │
                  └──► [1c] e1000 register constants
                             │
                             └──► [1d] e1000 init + rings ──► [1e] TX
                                       │                       │
                                       └──► [1f] RX (polling) ─┘
                                       │
                                       └──► [1g] IRQ handler
                                       │
                                       └──► [1h] Boot integration

Phase 2 (Ethernet + ARP):
  [2a] netutil (byte-order)
       │
       └──► [2b] Ethernet framing
                  │
                  ├──► [2c] ARP (cache + resolve)
                  │         │
                  │         └──► [2d] Gratuitous ARP
                  │
                  └──► [2e] RX dispatch goroutine

Phase 3 (IPv4 + ICMP + UDP):
  [3a] IPv4 parse/build (depends on 2b)
       │
       ├──► [3b] ICMP echo
       │
       └──► [3c] UDP (depends on 2c for ARP resolve)
       │
       └──► [3d] Static IP config
       │
       └──► [3e] Boot integration

Phase 4 (Robustness):
  [4a] Interrupt-driven RX (depends on 1g)
  [4b] Buffer pool (depends on 1d)
  [4c] Statistics counters (independent)
  [4d] Diagnostic output (depends on 4c)
  [4e] Error handling (cross-cutting)
```

Each phase depends on the previous one but can be merged
independently once its own test criteria pass.

## 7. LOC Estimates

| Phase | Component | Min LOC | Max LOC | New Files |
|---|---|---|---|---|
| 1 | PCI bus scan | 120 | 180 | `pci.go` |
| 1 | e1000 register constants | 80 | 120 | `e1000_regs.go` |
| 1 | e1000 init + TX/RX + IRQ | 400 | 600 | `e1000.go`, `e1000_irq.go` |
| 1 | `outl`/`inl` stubs | 20 | 30 | (edit `stubs.S`) |
| 2 | Byte-order utilities | 60 | 100 | `netutil.go` |
| 2 | Ethernet framing | 100 | 180 | `ethernet.go` |
| 2 | ARP | 200 | 350 | `arp.go` |
| 2 | RX dispatch + net init | 60 | 120 | `net.go` |
| 3 | IPv4 | 200 | 350 | `ipv4.go` |
| 3 | ICMP | 80 | 150 | `icmp.go` |
| 3 | UDP | 200 | 350 | `udp.go` |
| 4 | Buffer pool | 100 | 200 | `netbuf.go` |
| 4 | Statistics | 60 | 120 | `netstats.go` |
| 4 | Diagnostics | 100 | 250 | (edit existing or new ELF) |
| 4 | Error handling | 60 | 100 | (cross-cutting edits) |
| **Total** | | **1,840** | **3,200** | **10–12 new files** |

Changes to existing files:
- `src/stubs.S`: ~20 lines for `outl`/`inl`
- `src/main.go`: ~20 lines for `pciInit()`, `e1000Init()`,
  `netInit()` calls
- `src/ioapic.go`: ~5 lines for NIC IRQ redirection (if
  IOAPIC is re-enabled)

## 8. Design Decisions

| # | Decision | Rationale | Rejected Alternative |
|---|---|---|---|
| D1 | e1000 over virtio-net | e1000 is simpler (legacy descriptors, well-documented); virtio requires virtqueue negotiation | virtio-net (higher perf but more complex) |
| D2 | Legacy TX/RX descriptors | 16-byte fixed layout; no extended descriptor features needed for UDP | Extended descriptors (overkill for L4 UDP) |
| D3 | Raw `[16]byte` arrays for descriptors | TinyGo struct padding is unreliable for DMA-mapped structures | `struct` with explicit fields (may have padding) |
| D4 | Identity-mapped DMA buffers | gooos identity-maps low memory (0–1 GiB via 2 MiB pages in `boot.S`); `allocPagesContig` returns phys == virt | IOMMU / bounce buffers (unnecessary on QEMU) |
| D5 | Polling-first, IRQ later | Simpler debugging; known IOAPIC issue (`main.go:382-390`) | IRQ-first (blocked by IOAPIC bug) |
| D6 | Fixed-size ARP cache (16) | Sufficient for QEMU single-host testing; LRU replacement | Dynamic map (TinyGo map alloc may not be safe in ISR) |
| D7 | Fixed-size UDP bind table (8) | Minimal; one port per bound listener | Dynamic registration (complexity not justified) |
| D8 | Static IP configuration | No DHCP needed for QEMU testing | DHCP (500+ LOC, deferred) |
| D9 | No fragmentation | Drop fragments; UDP payload ≤ 1472 bytes sufficient | Reassembly (500+ LOC, deferred) |
| D10 | PIC pass-through for NIC IRQ | IOAPIC currently disabled (`main.go:389`); PIC is known working | IOAPIC (deferred until IOAPIC bug fixed) |
| D11 | Conservative GC safe | `allocPagesContig` memory is outside GC heap; conservative GC does not move objects | Special GC pinning (unnecessary) |
| D12 | Spinlock for shared net state | Consistent with existing kernel pattern (`pageAllocLock`, `procLock`, `gInfoLock`) | Channel-based serialization (higher overhead) |

## 9. Risk Register

| ID | Risk | Severity | Likelihood | Mitigation |
|---|---|---|---|---|
| R1 | TinyGo struct padding breaks descriptor DMA layout | High | Medium | Use raw `[16]byte` arrays with manual field pack/unpack; verify with `unsafe.Sizeof` |
| R2 | DMA buffer not in identity-mapped range | High | Low | `allocPagesContig` returns addresses within 0–1 GiB identity map; verify address < 0x40000000 |
| R3 | IOAPIC NIC IRQ delivery fails (known gooos IOAPIC bug) | Medium | High | Start with PIC pass-through or polling; defer IOAPIC to Phase 4 |
| R4 | Checksum off-by-one (endianness) | Medium | Medium | Byte-level tests with known-good pcap packet data |
| R5 | GC moves DMA buffers | High | None | Conservative GC does not relocate; `allocPagesContig` is outside GC heap |
| R6 | RX ring overflow under load | Low | Low | 64 descriptors × 1536 B = 96 KiB; ample for QEMU. Phase 4 adds drop counter. |
| R7 | Goroutine stack overflow in ISR | Medium | Medium | `//go:nosplit` on IRQ handler; minimal work (read ICR, send to channel, EOI) |
| R8 | `afterTicks` used for ARP timeout | Low | Low | Already tested and working in existing codebase |
| R9 | No `outl`/`inl` in `stubs.S` | Medium | High | Must be added (Phase 1a); straightforward assembly |
| R10 | e1000 EEPROM read hang | Low | Low | QEMU e1000 provides MAC in RAL/RAH registers directly; EEPROM read optional |
| R11 | PCI BAR0 at high address (>1 GiB) | Medium | Low | QEMU typically places e1000 BAR0 at ~0xFEBC0000; within mappable range. Log and assert. |
| R12 | Spinlock ordering violation with net locks | Medium | Low | Define lock ordering: `pageAllocLock(1) > netBufLock(5) > arpLock(6) > udpLock(7) > statsLock(8)` |

## 10. Lock Ordering Extension

Existing lock ordering (from `src/spinlock.go`):
1. `pageAllocLock`
2. `procLock`
3. `gInfoLock`
4. `vgaLock`

Extended for networking:
5. `netBufLock` — packet buffer pool
6. `arpLock` — ARP cache
7. `udpLock` — UDP bind table
8. `statsLock` — network statistics counters

A function holding lock N must not acquire lock M where M < N.

## 11. Changes to Existing Files

| File | Change | Lines |
|---|---|---|
| `src/stubs.S` | Add `outl` (32-bit out) and `inl` (32-bit in) | ~20 |
| `src/main.go` | Add `pciInit()`, `e1000Init()`, `netInit()` calls in boot sequence | ~15 |
| `src/ioapic.go` | Add NIC IRQ redirection entry (Phase 4, if IOAPIC re-enabled) | ~5 |
| `src/spinlock.go` | Update lock ordering comment to include net locks | ~4 |

## 12. QEMU Invocation

```bash
# User-mode networking (no root, limited to outbound + port forwarding):
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev user,id=n0,hostfwd=udp::9999-:7 \
  -no-reboot -no-shutdown

# TAP-based networking (requires root or CAP_NET_ADMIN):
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -no-reboot -no-shutdown

# With pcap dump for packet capture:
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev user,id=n0,dump=file:net.pcap \
  -no-reboot -no-shutdown
```

## 13. Future Extensions (Post-Phase 4)

| Extension | Complexity | Notes |
|---|---|---|
| TCP | ~2,000–4,000 LOC | Retransmission, window management, state machine |
| DHCP client | ~300–500 LOC | Builds on UDP; replaces static IP config |
| DNS resolver | ~200–400 LOC | Builds on UDP; stub resolver with upstream |
| virtio-net driver | ~400–800 LOC | Shared-memory virtqueues; higher throughput |
| IPv6 | ~500–1,000 LOC | NDP replaces ARP; 128-bit addresses |
| Socket syscall API | ~300–600 LOC | `sys_socket`, `sys_sendto`, `sys_recvfrom` for userland |

## 14. Open Questions

1. **NIC IRQ vector assignment**: PCI Interrupt Line register
   reports the legacy IRQ number (typically IRQ 11 for e1000
   on QEMU). With PIC pass-through, this maps to vector
   32+11=43. Verify this assumption during Phase 1.

2. **Multiple NICs**: The current design supports only one NIC.
   If QEMU presents multiple e1000 devices, we take the first
   one found during PCI scan.

3. **MAC address source**: QEMU e1000 stores the MAC in
   RAL0/RAH0 registers. Alternatively, read from EEPROM.
   Recommendation: read from RAL0/RAH0 first; fall back to
   EEPROM if RAL0 is zero.

4. **RX buffer size**: e1000 RCTL.BSIZE field selects 256, 512,
   1024, 2048, 4096, 8192, or 16384 bytes. We use 2048
   (default), which accommodates standard Ethernet MTU (1518)
   plus some headroom.

5. **Makefile `run-net` target**: Should we add a dedicated
   `run-net` target with `-device e1000`? Recommendation: yes,
   in Phase 1h.

## 15. Relationship to Existing Docs

- **`impldoc/smp_kernel_lapic_and_ipi.md §8`**: IOAPIC design.
  The NIC IRQ routing extends this; cross-referenced in
  `net_pci_e1000_driver.md §5`.
- **`impldoc/smp_percpu_and_sync.md §4`**: Spinlock ordering.
  Extended in §10 above for net locks.
- None of the existing design docs are modified; only
  cross-referenced.
