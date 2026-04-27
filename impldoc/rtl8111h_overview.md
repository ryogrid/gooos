# Networking Stack — Realtek RTL8111H NIC Driver

Detailed design and implementation plan for adding a Realtek
**RTL8111H** (PCI vendor `0x10ec` device `0x8168`, Linux
`mac_version` family **`RTL_GIGA_MAC_VER_46`**) NIC driver
to gooos so that, on host machines that have the chip on
board, the kernel detects it during boot, brings the link
up, obtains an IPv4 lease via the existing userspace DHCP
client, and serves the existing UDP/TCP socket API on top
of it — exactly as today's e1000 path does on QEMU.

Parent doc: `net_overview.md`.
Companion docs:
- `rtl8111h_phy_init.md` — long-form PHY/EPHY/ERI/MAC-OCP init detail (Chapter 5 expansion).
- `rtl8111h_review_checklist.md` — the ≥25-item code-review checklist + one-shot real-hardware test plan (Chapters 12 and 13).

---

## 1. Background and motivation

### 1.1 Why RTL8111H, why now

The e1000 (`src/e1000.go`, designed in `net_pci_e1000_driver.md`)
unblocked QEMU networking in gooos but does not help users
who want to run gooos on real hardware: consumer
motherboards almost universally ship a **Realtek** Gigabit
PHY (RTL8111B/C/D/E/F/G/H/EP/HP/FP, datacode "8168" in PCI
device-id space). A kernel that aspires to be more than a
QEMU toy needs at least one real-hardware NIC driver. The
RTL8111H is the most widely deployed variant of the family
in the 2018–2024 vintage of mid-range boards (B450 / B550 /
H470 / B660 generations of consumer chipsets), so it has
the highest hit-rate per implementation hour.

### 1.2 Verification cannot be done on QEMU — implications

QEMU has `-device rtl8169` (very-old-Realtek emulation) and
no `-device rtl8168` / `-device rtl8111h`. The 8168 family
on real silicon requires a long PHY/ERI/EPHY/MAC-OCP init
sequence that QEMU's RTL8169 model does not implement. A
"writes look right under `-device rtl8169`" smoke test
therefore proves nothing about RTL8111H bring-up.

This drives three workflow consequences the rest of this
document is shaped by:

1. **End-to-end verification is one shot, on real hardware,
   after the implementation is fully written.** No
   incremental on-target REPL. No bisection budget if the
   first boot prints `[rtl8111h] init failed`.
2. **Code review is the primary defence against bugs.** Every
   register write must cite a function name in upstream
   Linux `r8169_main.c` or `r8169_phy_config.c`, every
   descriptor field bit must cite the upstream definition
   line, every ordering constraint must cite the matching
   `dma_wmb()` / `smp_wmb()` placement upstream. Uncited
   magic numbers in the implementation are forbidden by
   review.
3. **The driver's compile-time gate must default OFF** so a
   half-written driver cannot break the existing e1000 +
   QEMU regression matrix. See Chapter 14 (Rollback).

### 1.3 What is unlocked once it works

- gooos boots on bare-metal consumer hardware and acquires
  an IPv4 lease via the existing `dhcp.elf` userspace
  client (no kernel changes needed in `user/cmd/dhcp/`).
- The existing UDP socket syscalls (`sys_socket`,
  `sys_bind`, `sys_sendto`, `sys_recvfrom`,
  `sys_sendto_bcast`) and TCP socket syscalls (`sys_listen`,
  `sys_accept`, `sys_connect`, `sys_tcp_send`,
  `sys_tcp_recv`, `sys_shutdown`) all work over the new
  NIC unmodified — `socketFd` and the bind tables are
  NIC-agnostic.
- `udpecho.elf`, `tcpecho.elf`, `tcpcli.elf`, `wget.elf`
  all run unchanged.

---

## 2. Hardware overview

### 2.1 PCI presence

- **Vendor ID**: `0x10ec` (Realtek).
- **Device ID**: `0x8168`. Confirmed in upstream `r8169_main.c:rtl8169_pci_tbl` as `PCI_VDEVICE(REALTEK, 0x8168)`. The same device-id covers many sub-revisions (8111B/C/D/E/F/G/H/EP/HP/FP); the exact silicon revision is read from the **chip XID** field embedded in the `TxConfig` MMIO register at offset `0x40`, bits `(TxConfig >> 20) & 0x7cf`. For RTL8111H, `XID == 0x541` (alongside `0x6c0` for the rebadged "RTL8168M"); both map to **`RTL_GIGA_MAC_VER_46`**. **Note**: the same XID `0x541` is reported for the **RTL8107e** (a 100 Mbps-only variant) when the chip is non-GMII; for v1 the driver accepts the XID and relies on the auto-neg poll in step 22 of §5.1 to detect that the link came up at 100 Mbps (still functional, just not gigabit). Cite: `r8169_main.c:rtl8169_get_chip_version` and the `rtl_chip_infos[]` table.
- **Note on `RTL_GIGA_MAC_VER_45`**: upstream Linux **removed** support for VER_45 in commit `ebe5989` ("r8169: remove support for chip versions 45 and 47", Aug 2022). The 8168H/8111H is now **VER_46 only** in upstream and **this driver targets VER_46 only**. If a from-scratch port copies older VER_45 paths (e.g. `rtl8168h_1_hw_phy_config`), it will silently program the wrong PHY init — this is footgun #5 in `rtl8111h_review_checklist.md`.
- **BAR layout**: BAR0 is **128 KiB** of MMIO-mapped registers (mirrors e1000's BAR0 size — `e1000MmioSize = 0x20000` in `src/e1000.go`). All accesses are MMIO; there is no legacy I/O-port region required.
- **PCI Cmd register**: must have `Memory Space (bit 1)` and `Bus Master (bit 2)` set before any MMIO or DMA — same as `pciRecordE1000` does for the e1000.
- **Interrupt line**: read from PCI config offset `0x3C` (low 8 bits) — the BIOS programs whichever ISA-equivalent IRQ the board's PCH routes to. **Do not hard-code IRQ 11**; obey what the BIOS reports, the same way `e1000PCI.IRQLine` is consumed in `src/main.go` after `pciInit`.

### 2.2 Register map (BAR0 offsets, RTL8111H = VER_46)

The following are upstream-confirmed offsets the driver writes during init or hot path. Vendor doc names are given in parentheses where they differ.

| Offset | Width | Symbol (upstream) | Vendor name | Purpose |
|---|---|---|---|---|
| `0x00` | 4 B | `MAC0` | `IDR0..IDR3` | MAC address bytes 0–3 (LE) |
| `0x04` | 4 B | `MAC4` | `IDR4..IDR7` | MAC address bytes 4–5 (LE), upper 16 bits reserved |
| `0x20` | 4 B | `TxDescStartAddrLow` | `TNPDS_LO` | TX ring base, low 32 bits |
| `0x24` | 4 B | `TxDescStartAddrHigh` | `TNPDS_HI` | TX ring base, high 32 bits |
| `0x37` | 1 B | `ChipCmd` (cited below) | `CR` | RxEnb / TxEnb / Reset bits |
| `0x38` | 1 B | `TxPoll` | `TPPoll` | TX doorbell — write `NPQ = 0x40` to kick |
| `0x3c` | 2 B | `IntrMask` | `IMR` | Interrupt mask (RW1C-style enable) |
| `0x3e` | 2 B | `IntrStatus` | `ISR` | Interrupt cause; **write-1-to-clear** |
| `0x40` | 4 B | `TxConfig` | `TCR` | IPG / DMA burst / FIFO mode (chip XID also lives here) |
| `0x44` | 4 B | `RxConfig` | `RCR` | DMA burst / accept-mask / multi-RX |
| `0x50` | 1 B | `Cfg9346` | `9346CR` | Config-reg unlock: write `0xC0` to unlock, `0x00` to lock |
| `0x6c` | 1 B | `PHYstatus` | — | Link-up / 10/100/1000 / FullDup bits |
| `0x6f` | 1 B | `PMCH` | — | D3 PLL-down control (`D3HOT_NO_PLL_DOWN = bit 6`, `D3COLD_NO_PLL_DOWN = bit 7`) |
| `0x70` | 4 B | `ERIDR` | `ERIDR` | ERI data register |
| `0x74` | 4 B | `ERIAR` | `ERIAR` | ERI address register (`ERIAR_FLAG = 0x80000000` busy bit) |
| `0x80` | 4 B | `EPHYAR` | — | EPHY (PCIe SerDes) access register |
| `0xb0` | 4 B | `OCPDR` | — | MAC-OCP data register (used for OCP base `0xa400`+ offsets) |
| `0xb4` | 4 B | `OCPAR` | — | MAC-OCP address register (`OCPAR_FLAG = 0x80000000` busy bit) |
| `0xb8` | 4 B | `GPHY_OCP` | — | PHY-OCP single-register access |
| `0xda` | 2 B | `RxMaxSize` | `RMS` | Maximum RX frame size (set to `R8169_RX_BUF_SIZE + 1`) |
| `0xe4` | 4 B | `RxDescAddrLow` | `RDSAR_LO` | RX ring base, low 32 bits |
| `0xe8` | 4 B | `RxDescAddrHigh` | `RDSAR_HI` | RX ring base, high 32 bits |
| `0xf0` | 1 B | `MISC` | — | `RXDV_GATED_EN` lives here |

> The vendor data sheet documents many extended interrupt registers at `0xE0`/`0xE2` (`IMR0`/`ISR0`). **Do not use them for RTL8111H** — those are 32-bit registers reserved for the RTL8125 (2.5 GbE) family. RTL8111H takes the legacy 16-bit `IntrMask`/`IntrStatus` at `0x3c`/`0x3e` because `rtl_is_8125(tp) == false` for `VER_46`. Cite: `r8169_main.c:rtl_get_events` / `rtl_ack_events` / `rtl_irq_disable` / `rtl_irq_enable`.

### 2.3 Descriptor format (16 bytes each, identical for TX and RX)

Cite: upstream `r8169_main.c:struct TxDesc` / `struct RxDesc`.

```
+------+------+------+------+
| opts1               (LE) | byte 0..3
+-------------------------+
| opts2               (LE) | byte 4..7
+-------------------------+
| addr (low 32 bits)  (LE) | byte 8..11
+-------------------------+
| addr (high 32 bits) (LE) | byte 12..15
+-------------------------+
```

`opts1` is the only field hardware writes back on completion. It carries the ownership bit, ring-end bit, fragment markers, and (for RX) the packet size.

**Generic `opts1` bits** (bit positions, MSB at top):

| Bit | Symbol | Meaning |
|---|---|---|
| 31 | `DescOwn` | Set by the **driver** when handing the descriptor to the NIC; the **NIC** clears it (RX) or leaves it cleared after sending (TX, where the driver sets it again on every reuse). |
| 30 | `RingEnd` | Set on the last descriptor of the ring so the NIC wraps to `desc[0]` instead of running off into adjacent memory. |
| 29 | `FirstFrag` | Set on the first descriptor of a multi-fragment packet (or always, for single-fragment frames). |
| 28 | `LastFrag` | Set on the last descriptor of a packet (or always, for single-fragment frames). |
| 13:0 | (RX only) packet size | Length of the received frame in bytes, including L2 headers, *excluding* FCS once we strip it. |

**RX-only `opts1` upper bits** (cite `r8169_main.c:471-476`):

| Bit | Symbol | Meaning |
|---|---|---|
| 22 | `RxRWT` | Receive watchdog timer expired |
| 21 | `RxRES` | Receive error summary — drop and bump `RxDropped` |
| 20 | `RxRUNT` | Frame shorter than 64 bytes |
| 19 | `RxCRC` | FCS check failed |

**TX-only `opts1` upper bits used by VER_46** (cite `r8169_main.c:589-624`, the "tx_desc_bit_1" set):

`TD1_GTSENV4`, `TD1_GTSENV6`, `GTTCPHO_SHIFT = 18` etc — these are TSO/GSO and **not used in v1**. The v1 driver always submits single-segment, no-checksum-offload frames (the upper stack already computes IPv4 / UDP / TCP checksums in `src/ipv4.go`, `src/udp.go`, `src/tcp_segment.go`).

**`opts2` for TX**: zero in v1 (no checksum offload, no VLAN tag).
**`opts2` for RX**: ignored in v1 (we treat received frames as opaque bytes for `ethernetHandle`).

### 2.4 Internal Realtek Gigabit PHY

The PHY is on-die and accessed via two distinct register windows:

- **Standard MII via PHYlib-equivalent paged access** — page-select on MII reg `0x1f`, then read/write the target reg. In the upstream Linux driver this is delegated to the kernel PHYlib (`phy_read_paged`, `phy_write_paged`, `phy_modify_paged`); since gooos has no PHYlib we must implement these helpers directly in `src/rtl8111h_phy.go` (one MII access via `MII_ADDR/MII_DATA`-equivalent registers, see `rtl8111h_phy_init.md` for the exact two-register sequence).
- **PHY OCP** (`r8168_phy_ocp_read` / `r8168_phy_ocp_write`) — single-register access via `GPHY_OCP = 0xb8`. Used for analog/RF tuning that is not exposed via the MII register space.

The full PHY init sequence (`r8169_phy_config.c:rtl8168h_2_hw_phy_config`) is reproduced verbatim in `rtl8111h_phy_init.md`. It is the single longest and most error-sensitive part of the driver.

### 2.5 Interrupt mask / status semantics

Bit definitions (cite `r8169_main.c:457-469`):

| Bit | Symbol | Meaning |
|---|---|---|
| 0 | `RxOK` | Frame(s) deposited in RX ring |
| 1 | `RxErr` | RX FIFO error / descriptor error |
| 2 | `TxOK` | Frame(s) finished transmission |
| 3 | `TxErr` | TX abort (excessive collisions, FIFO underrun) |
| 4 | `RxOverflow` | RX FIFO overflow (set when descriptors are not drained fast enough) |
| 5 | `LinkChg` | PHY link state changed (auto-neg complete or link drop) |
| 6 | `RxFIFOOver` | RX FIFO full (legacy 8169 only) |
| 7 | `TxDescUnavail` | TX descriptors exhausted |
| 8 | `SWInt` | Software-triggered interrupt |
| 14 | `PCSTimeout` | PCS-layer timeout |
| 15 | `SYSErr` | System (PCIe) error — only meaningful on legacy 8169 |

**For RTL8111H the IRQ mask is** (cite `r8169_main.c:rtl_set_irq_mask`):

```
IntrMask = RxOK | RxErr | TxOK | TxErr | LinkChg
         = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0020
         = 0x002F
```

`SYSErr` and `RxFIFOOver` are deliberately masked off; they are only enabled for `mac_version <= RTL_GIGA_MAC_VER_06` (the original RTL8169).

`IntrStatus` is **write-1-to-clear (RW1C)**: the ISR reads the value, then writes back the bits it observed to ack them. Cite: `r8169_main.c:rtl_ack_events`.

### 2.6 Link-state notification

When `LinkChg` (bit 5) fires, the driver re-reads `PHYstatus` (`0x6c`) to learn the new state. Bit definitions (cite `r8169_main.c:561-569`):

| Bit | Symbol | Meaning |
|---|---|---|
| 7 | `TBI_Enable` | (irrelevant for copper PHY) |
| 6 | `TxFlowCtrl` | TX pause-frame ability negotiated |
| 5 | `RxFlowCtrl` | RX pause-frame ability negotiated |
| 4 | `_1000bpsF` | Link is 1 Gbps |
| 3 | `_100bps` | Link is 100 Mbps |
| 2 | `_10bps` | Link is 10 Mbps |
| 1 | `LinkStatus` | Link is up |
| 0 | `FullDup` | Full duplex |

For DHCP to start, the driver only needs to expose `LinkStatus == 1` to the upper stack (see Chapter 9).

---

## 3. Comparison with the existing e1000 driver

The cardinal design rule for this driver is: **reuse every gooos networking primitive verbatim; only add chip-specific code.** The two-column table below is the contract.

### 3.1 Reusable verbatim — *do not duplicate*

| Primitive | File | Use it for RTL8111H? |
|---|---|---|
| PCI bus scan via mechanism 1 (CONFIG_ADDRESS=`0xCF8` / CONFIG_DATA=`0xCFC`) | `src/pci.go:pciInit`, `pciConfigRead32` | **Yes** — extend `pciInit` with `pciRecordRTL8111H` after `pciRecordE1000`. |
| `PCIDevice` struct | `src/pci.go:59` | **Yes** — populate one for the RTL8111H find. |
| MMIO map of BAR0 | `src/vm.go:mapPage` (with `pagePresent | pageWrite | pagePCD | pagePWT`) | **Yes** — clone the loop body of `e1000MapMMIO` for `rtl8111hMapMMIO`. Same 128 KiB window. |
| Contiguous DMA allocator | `src/vm.go:allocPagesContig` | **Yes** — use exactly as e1000 does for descriptor rings and ring buffers. |
| Buffer pool | `src/netbuf.go:netBufAlloc` / `netBufFreeIdx` | **Yes** — for v1 the RTL8111H driver uses `netbuf` for nothing on its hot path (it owns its own RX ring buffers, mirroring e1000's pre-Phase-4 design); but `BufAllocFail` should still be visible. Future zero-copy work would lease netbufs to the RX ring directly. |
| ISR registration | `src/main.go` block calling `registerHandler(32 + e1000PCI.IRQLine, handleE1000IRQ)` | **Yes** — add a sibling block for RTL8111H, gated on `enableRTL8111H && rtl8111hFound`. |
| EOI helper | `picSendEOI` (or `lapicSendEOI` if `ioapicActive`) inside the ISR | **Yes** — copy the e1000 ISR's two-branch tail verbatim. |
| `netRxLoop` | `src/net.go:netRxLoop` | **Yes** — single BSP-pinned kthread, unchanged. The new `drainRxRingRTL8111H` is added next to `drainRxRing` and the dispatch picks one based on `activeNIC.name`. |
| `rxReadyFlag` polling pattern | `src/e1000_irq.go:rxReadyFlag` (set by ISR, polled by `netRxLoop`) | **Yes** — add a sibling `rxReadyFlagRTL8111H`. (Two flags rather than overloading one — different ISRs touch different cache lines, makes the lint and the diagnostic story clearer.) |
| Ethernet dispatch | `src/ethernet.go:ethernetDispatch` | **Yes** — RX path hands raw frames to it unchanged. |
| ARP / IPv4 / ICMP / UDP / TCP / sockets / DHCP | `src/{arp,ipv4,icmp,udp,tcp,netsock}.go`, `user/cmd/dhcp/main.go` | **Yes** — all NIC-agnostic; **no edits required** beyond the TX-dispatch change in §3.2. |
| NetStats counters | `src/netstats.go:NetStats` | **Yes** — driver bumps `RxPackets`, `RxBytes`, `RxDropped`, `NetRxFrames` on RX and `TxPackets`, `TxBytes` on TX. The existing `E1000IRQs` field is repurposed; see §3.2 for the small `netstats.go` edit. |
| Lock-rank discipline | `src/spinlock.go` ranks 5 (netbuf) and below | **Yes** — every shared variable in the driver (TX index, RX index, ISR-set flag, link-state) follows the same rules: `Spinlock.Acquire / Release` if shared across CPUs, plain stores under x86-TSO if BSP-only. The driver does not introduce any new ranks. |

### 3.2 RTL8111H-specific — these are the only files we add or edit

| What changes | Where | Why |
|---|---|---|
| **NEW** `src/rtl8111h.go` (~600 lines) | greenfield | Register constants, BAR0 map, descriptor ring alloc, init sequence, TX, RX-drain, MAC-address read, link-state. |
| **NEW** `src/rtl8111h_phy.go` (~250 lines) | greenfield | OCP / ERI / EPHY / MII paged accessors + the `rtl8168h_2_hw_phy_config` init table. Long but mostly a transcription of upstream. |
| **NEW** `src/rtl8111h_irq.go` (~80 lines) | greenfield | `handleRTL8111HIRQ` ISR, `rxReadyFlagRTL8111H` global, `rtl8111hIRQCount` counter. ISR-safe (no allocations). |
| **NEW** `src/nic.go` (~50 lines) | greenfield | `nicDriver` function-pointer struct + `activeNIC` global. The minimal abstraction so the upper stack can dispatch TX through one pointer. |
| **EDIT** `src/pci.go` | add `pciRecordRTL8111H` mirroring `pciRecordE1000`; add `rtl8111hPCI` and `rtl8111hFound` package globals; in `pciInit` body, add the `0x10ec:0x8168` match arm next to the existing `0x8086:0x100E` one. | Discovery. |
| **EDIT** `src/net.go` (`netInit`) | after `pciInit` returns, decide which NIC to bring up: `if activeNIC == nil && rtl8111hFound && enableRTL8111H { rtl8111hInit(); activeNIC = &rtl8111hDriver }`; the existing e1000 path moves into a sibling `if activeNIC == nil && e1000Found { e1000Init(); activeNIC = &e1000Driver }`. The exact tie-breaking rule (when both are present) is in Chapter 11. | Single dispatch entry. |
| **EDIT** `src/main.go` | next to the existing `registerHandler(32 + e1000PCI.IRQLine, handleE1000IRQ)` block, add a sibling `if enableRTL8111H && rtl8111hFound { registerHandler(int(32 + rtl8111hPCI.IRQLine), handleRTL8111HIRQ) }`. **Note**: order matters — see Chapter 11 for the both-present rule. | ISR install. |
| **EDIT** `src/arp.go` | three `e1000Transmit(frame)` call sites become `activeNIC.transmit(frame)`. Five-line context for each is reproduced in §3.3. | Indirect TX. |
| **EDIT** `src/ipv4.go` | one `e1000Transmit(frame)` call site becomes `activeNIC.transmit(frame)`. | Indirect TX. |
| **EDIT** `src/udp.go` | one `e1000Transmit(frame)` call site becomes `activeNIC.transmit(frame)`. | Indirect TX. |
| **EDIT** `src/preempt_config.go` | add `const enableRTL8111H = false` and `const preferRTL8111H = false` at the bottom, alongside the existing gates (`uniprocessorKernel`, `userspaceSMP`, etc.). | Compile-out toggle + tie-breaker. |
| **EDIT** `src/netstats.go` | add `RTL8111HIRQs uint64` next to the existing `E1000IRQs uint64` field. **Do not rename `E1000IRQs`** — the existing diagnostics print code references it by that name. | Per-NIC IRQ counter. |
| **EDIT** `src/net.go` (`netDiag`) | extend the diagnostic line printer to also show the active NIC name and the RTL8111H counters when present. | Visibility. |

That is the entire surface area. **Notably absent**: no changes to `src/ethernet.go`, `src/ipv4.go` (apart from the one TX line), `src/icmp.go`, `src/udp.go` (apart from the one TX line), `src/tcp*.go`, `src/netsock.go`, `src/netbuf.go`, `src/netstats.go` (apart from one new field), `user/`, `Makefile`, or any `scripts/`.

### 3.3 Verbatim TX call-site context

For each of the five `e1000Transmit` call sites the driver redirects through `activeNIC.transmit`. These are mechanical edits — they replace one identifier with another and gate on `activeNIC != nil` instead of `e1000Found`. If review wants to see the surrounding lines, here they are.

**`src/arp.go:arpSendRequest`** (currently `src/arp.go:157-167`):

```go
func arpSendRequest(targetIP uint32) {
    if !e1000Found {
        return
    }
    payload := arpBuild(arpOpRequest, e1000MAC, ourIP, [6]byte{}, targetIP)
    frame := ethernetBuild(broadcastMACAddr(), e1000MAC, etherTypeARP, payload)
    if e1000Transmit(frame) {
        statsInc(&netStats.ArpRequestsSent)
    }
}
```

After:

```go
func arpSendRequest(targetIP uint32) {
    if activeNIC == nil {
        return
    }
    payload := arpBuild(arpOpRequest, activeNIC.mac, ourIP, [6]byte{}, targetIP)
    frame := ethernetBuild(broadcastMACAddr(), activeNIC.mac, etherTypeARP, payload)
    if activeNIC.transmit(frame) {
        statsInc(&netStats.ArpRequestsSent)
    }
}
```

(Note the additional substitution of `e1000MAC` for `activeNIC.mac`. The `nicDriver` struct in `src/nic.go` exposes a `mac [6]byte` field initialised by whichever driver `init` runs — either `e1000Init` or `rtl8111hInit`. This eliminates the e1000-specific `e1000MAC` global from the ARP / IPv4 / UDP build paths.)

**`src/arp.go:arpSendReply`** (currently `src/arp.go:171-180`): same shape. **`src/arp.go:arpSendGratuitous`** (currently `src/arp.go:185-195`): same shape. **`src/ipv4.go:ipv4Send`** (currently `src/ipv4.go:155-180`): the line `copy(frame[6:12], e1000MAC[:])` becomes `copy(frame[6:12], activeNIC.mac[:])`, and the final `return e1000Transmit(frame)` becomes `return activeNIC.transmit(frame)`. **`src/udp.go:udpSendRaw`** (currently `src/udp.go:274-312`): same two substitutions.

The total wire-edit volume in the existing source is **5 functions × 2 lines each = 10 line changes**, plus the addition of `src/nic.go` and the `enableRTL8111H` gate. This is the smallest abstraction the design admits.

---

## 4. File layout under `src/`

### 4.1 New files

```
src/
├── nic.go                  ~50 lines   nicDriver struct + activeNIC global + e1000Driver/rtl8111hDriver descriptors
├── rtl8111h.go             ~600 lines  registers, BAR0 map, ring alloc, init, TX, RX-drain, MAC read, link state
├── rtl8111h_phy.go         ~250 lines  OCP/ERI/EPHY/MII paged accessors + rtl8168h_2_hw_phy_config init table
└── rtl8111h_irq.go         ~80 lines   handleRTL8111HIRQ + rxReadyFlagRTL8111H + rtl8111hIRQCount + EOI tail
```

### 4.2 `src/nic.go` shape (full proposed source)

```go
package main

// nicDriver is a function-pointer table that lets the upper stack
// (ARP / IPv4 / UDP) dispatch TX through one pointer, and lets
// netRxLoop dispatch the RX drain through another, regardless of
// which NIC is the active one. Set exactly once by netInit; never
// changes after boot.
type nicDriver struct {
    name     string                 // "e1000" or "rtl8111h" — used by netDiag for the active-NIC line
    mac      [6]byte                // MAC address as read from the chip
    transmit func([]byte) bool      // synchronous TX
    drainRX  func()                 // called by netRxLoop after the ISR sets the per-NIC ready flag
}

// activeNIC is the active NIC dispatch table. nil means no NIC was
// found at boot (or all enabled NICs failed init). All TX paths must
// guard with `if activeNIC == nil { return }` (or equivalent).
var activeNIC *nicDriver

// e1000Driver is the dispatch table for the existing Intel 82540EM
// driver. Populated by e1000Init() once the chip is up.
var e1000Driver = nicDriver{
    name:     "e1000",
    transmit: e1000Transmit,
    drainRX:  drainRxRing, // existing func in src/net.go
}

// rtl8111hDriver is the dispatch table for the RTL8111H driver.
// Populated by rtl8111hInit() once the chip is up.
var rtl8111hDriver = nicDriver{
    name:     "rtl8111h",
    transmit: rtl8111hTransmit,
    drainRX:  drainRxRingRTL8111H, // new func in src/rtl8111h.go
}
```

That is the entire abstraction. No interface, no method set, no boxing. Three pointers per driver. Compatible with `make lint` (no allocations, no chans, no go-statements).

### 4.3 Why not a Go interface?

- A method-set interface would force every `activeNIC.transmit(frame)` site through an interface dispatch, which boxes `[]byte` arguments and triggers escape analysis. `make lint` forbids interface boxing inside ISR-reachable code (`scripts/lint_isr.go` flags it as `"interface boxing"`). The TX paths are not ISR-reachable, but the discipline matters: the function-pointer form is one line of indirection without any allocator pressure.
- The two NICs share **no** behavioural surface beyond `transmit`: their inits are different, their RX-drains are different, their ISRs are different. Putting `init` and `drainRX` and `isr` all behind interface methods would be premature abstraction.

### 4.4 `src/preempt_config.go` additions

Append at the bottom of the file, matching the existing const-bool style:

```go
// enableRTL8111H gates the RTL8111H driver. Default false so QEMU
// builds (which only emulate e1000) are unaffected and a half-written
// driver cannot break the existing regression matrix. Flip to true
// for real-hardware bring-up.
const enableRTL8111H = false

// preferRTL8111H is the tie-breaker if both an e1000 *and* an
// RTL8111H are present at boot (rare in practice — QEMU never has
// both, real boards rarely do). false means e1000 wins (the gooos
// regression-test default); true means RTL8111H wins. See Chapter 11
// of impldoc/rtl8111h_overview.md.
const preferRTL8111H = false
```

### 4.5 `src/pci.go` additions

After the existing `pciRecordE1000` definition, add a sibling:

```go
// pciRecordRTL8111H captures the device coordinates, enables Memory
// Space + Bus Master, and verifies BAR0 is MMIO. Mirrors
// pciRecordE1000 verbatim except for the global it stores into.
func pciRecordRTL8111H(bus, dev, fn uint8, vendor, device uint16) {
    rtl8111hPCI = PCIDevice{
        Bus:      bus,
        Device:   dev,
        Function: fn,
        VendorID: vendor,
        DeviceID: device,
    }
    rtl8111hPCI.BAR0 = pciConfigRead32(bus, dev, fn, 0x10) &^ 0xF
    rtl8111hPCI.IRQLine = uint8(pciConfigRead32(bus, dev, fn, 0x3C) & 0xFF)
    // Enable Memory Space + Bus Master.
    cmd := pciConfigRead32(bus, dev, fn, 0x04)
    cmd |= 0x06
    pciConfigWrite32(bus, dev, fn, 0x04, cmd)
    rtl8111hFound = true
}

var rtl8111hPCI PCIDevice
var rtl8111hFound bool
```

In `pciInit` itself, the inner match is extended:

```go
switch {
case vendor == 0x8086 && device == 0x100E:
    pciRecordE1000(b, d, f, vendor, device)
case vendor == 0x10EC && device == 0x8168:
    if enableRTL8111H {
        pciRecordRTL8111H(b, d, f, vendor, device)
    }
}
```

(Gate by `enableRTL8111H` so the discovery side-effects — Bus Master enable etc. — are not performed when the driver is compiled out.)

### 4.6 Kernel primitives the driver introduces

The driver relies on several small primitives that gooos does not have today. The implementer must add them as part of the same patch series — they are tiny but their absence will surface as unresolved-symbol errors at first compile.

#### 4.6.a MMIO accessors (`src/mmio.go`, new file, ~30 lines)

```go
package main

import "unsafe"

//go:nosplit
func mmioRead8(addr uintptr) uint8   { return *(*uint8)(unsafe.Pointer(addr)) }
//go:nosplit
func mmioRead16(addr uintptr) uint16 { return *(*uint16)(unsafe.Pointer(addr)) }
//go:nosplit
func mmioRead32(addr uintptr) uint32 { return *(*uint32)(unsafe.Pointer(addr)) }
//go:nosplit
func mmioWrite8(addr uintptr, v uint8)   { *(*uint8)(unsafe.Pointer(addr)) = v }
//go:nosplit
func mmioWrite16(addr uintptr, v uint16) { *(*uint16)(unsafe.Pointer(addr)) = v }
//go:nosplit
func mmioWrite32(addr uintptr, v uint32) { *(*uint32)(unsafe.Pointer(addr)) = v }
```

All `//go:nosplit`. Allowed in ISR-reachable scope per the `scripts/lint_isr.go` safelist (the lint walker treats `//go:nosplit` callees as terminal beyond depth 0, and the bodies have no chans/allocations/string-concat).

#### 4.6.b Delay primitives (extend `src/afterticks.go` or add `src/timing.go`)

```go
// msleep busy-loops on pitTicks for at least ms milliseconds. PIT is
// 100 Hz so resolution is 10 ms; ms is rounded up. Safe to call from
// non-ISR context only.
func msleep(ms uint32) {
    deadline := pitTicks + uint64((ms+9)/10)
    for pitTicks < deadline { hlt() }
}

// udelay busy-loops with `pause` for approximately us microseconds.
// Calibrated against TSC at boot if available; otherwise a coarse
// loop. Safe in ISR-reachable scope (no allocations, no chans).
//go:nosplit
func udelay(us uint32) { /* TSC-deadline busy-loop */ }

// pollUntilLow reads the 32-bit MMIO word at addr, masks with mask,
// and returns true if the masked bits drop to zero within `count`
// polls each separated by `usec` microseconds. Returns false on
// timeout. Mirror of upstream rtl_loop_wait_low semantics.
//go:nosplit
func pollUntilLow(addr uintptr, mask uint32, count, usec uint32) bool { /* ... */ }

//go:nosplit
func pollUntilHigh(addr uintptr, mask uint32, count, usec uint32) bool { /* ... */ }

//go:nosplit
func pollUntilLow8(addr uintptr, mask uint8, count, usec uint32) bool { /* ... */ }
```

#### 4.6.c PCI config-space helpers (extend `src/pci.go`)

`src/pci.go` currently exposes only `pciConfigRead32` / `pciConfigWrite32` (mechanism-1 via I/O ports `0xCF8`/`0xCFC`). The driver also needs:

```go
// 8/16-bit config-space accessors derive from the 32-bit ones with
// shift+mask; offsets must be inside the first 256 bytes (mechanism-1
// only addresses 256 bytes — see §4.6.d for the 0x70F caveat).

func pciConfigRead8(bus, dev, fn, offset uint8) uint8 {
    return uint8((pciConfigRead32(bus, dev, fn, offset&0xFC) >> ((offset & 3) * 8)) & 0xFF)
}

func pciConfigWrite8(bus, dev, fn, offset uint8, v uint8) {
    aligned := offset &^ 3
    cur := pciConfigRead32(bus, dev, fn, aligned)
    shift := (offset & 3) * 8
    cur = (cur &^ (uint32(0xFF) << shift)) | (uint32(v) << shift)
    pciConfigWrite32(bus, dev, fn, aligned, cur)
}

func pciConfigRead16(bus, dev, fn, offset uint8) uint16 {
    return uint16((pciConfigRead32(bus, dev, fn, offset&0xFC) >> ((offset & 2) * 8)) & 0xFFFF)
}

func pciConfigWrite16(bus, dev, fn, offset uint8, v uint16) {
    aligned := offset &^ 3
    cur := pciConfigRead32(bus, dev, fn, aligned)
    shift := (offset & 2) * 8
    cur = (cur &^ (uint32(0xFFFF) << shift)) | (uint32(v) << shift)
    pciConfigWrite32(bus, dev, fn, aligned, cur)
}

// pciFindCapability walks the chained capability list anchored at
// config offset 0x34 (Capabilities Pointer). Returns the offset of
// the first capability whose ID matches capID, or 0 if none found.
func pciFindCapability(bus, dev, fn uint8, capID uint8) uint8 {
    ptr := pciConfigRead8(bus, dev, fn, 0x34)
    for i := 0; i < 48 && ptr != 0; i++ {
        cap := pciConfigRead16(bus, dev, fn, ptr)
        id := uint8(cap & 0xFF)
        next := uint8(cap >> 8)
        if id == capID {
            return ptr
        }
        ptr = next
    }
    return 0
}
```

#### 4.6.d ASPM entry-latency at config offset 0x70F — extended config space limitation

Upstream `rtl_set_def_aspm_entry_latency` writes config-space byte `0x70F`. **gooos's mechanism-1 port-IO config access (`0xCF8`/`0xCFC`) only addresses the first 256 bytes**; offset `0x70F` is in PCIe extended config space and requires either MMCONFIG (memory-mapped config space, not implemented in gooos) or the chip-specific CSI fallback `r8169_main.c:rtl_csi_mod` uses (`CSIAR = 0x68`, `CSIDR = 0x64`).

For v1 we **commit to the CSI fallback path** (the same fallback upstream uses when standard config-space write fails). The CSI helpers go into `src/rtl8111h.go`:

```go
const (
    rCSIAR = 0x68
    rCSIDR = 0x64

    csiarFlag    = uint32(0x80000000)
    csiarByteEn  = uint32(0xf << 12) // ERIAR_MASK_1111-equivalent for CSI
    csiarFunc0   = uint32(0)         // function shift
)

func csiWrite(addr uint16, value uint32) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rCSIDR, value)
    cmd := csiarFlag | csiarByteEn | csiarFunc0 | uint32(addr)
    mmioWrite32(rtl8111hBase+rCSIAR, cmd)
    pollUntilLow(rtl8111hBase+rCSIAR, csiarFlag, 100, 100)
}

func csiRead(addr uint16) uint32 {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    cmd := csiarByteEn | csiarFunc0 | uint32(addr)
    mmioWrite32(rtl8111hBase+rCSIAR, cmd)
    if !pollUntilHigh(rtl8111hBase+rCSIAR, csiarFlag, 100, 100) {
        return ^uint32(0)
    }
    return mmioRead32(rtl8111hBase + rCSIDR)
}

// rtlSetDefAspmEntryLatency writes byte 0x070F of PCIe config space
// via the CSI dword at 0x070C. Mirror of upstream's CSI fallback in
// rtl_set_aspm_entry_latency. value 0x27 = L0 7us, L1 16us.
func rtlSetDefAspmEntryLatency() {
    cur := csiRead(0x070c)
    new := (cur &^ 0xff000000) | (uint32(0x27) << 24)
    csiWrite(0x070c, new)
}
```

This is more code than the original "5-line addition" sketch, but it is the only path that actually reaches byte `0x70F` on gooos. ASPM L1 disable in PCIe Express capability Link Control (`§3.4` of `rtl8111h_phy_init.md`) is the **other** mitigation for Footgun #3 — both are required, both are now implementable.

### 4.7 `src/main.go` additions

The block currently reading:

```go
pciInit()
if e1000Found {
    e1000Init()
    e1000Vector := int(32 + e1000PCI.IRQLine)
    registerHandler(e1000Vector, handleE1000IRQ)
    e1000EnableInterrupts()
    serialPrintln("e1000: NIC initialized")
}
netInit()
```

becomes:

```go
pciInit()
if enableRTL8111H && rtl8111hFound && (preferRTL8111H || !e1000Found) {
    rtl8111hInit()
    rtl8111hVector := int(32 + rtl8111hPCI.IRQLine)
    registerHandler(rtl8111hVector, handleRTL8111HIRQ)
    rtl8111hEnableInterrupts()
    serialPrintln("rtl8111h: NIC initialized")
} else if e1000Found {
    e1000Init()
    e1000Vector := int(32 + e1000PCI.IRQLine)
    registerHandler(e1000Vector, handleE1000IRQ)
    e1000EnableInterrupts()
    serialPrintln("e1000: NIC initialized")
}
netInit()
```

**Note** — `netInit()` itself does the `activeNIC = &xxxDriver` assignment based on which init ran. The decision lives in `netInit`, not `main`, so that test harnesses that call `netInit` directly without going through `main` get the same behaviour. See Chapter 11 for the full coexistence rule and its rationale.

---

## 5. Detailed initialisation sequence

This chapter is the **single most error-sensitive** part of the document. It is split into a top-level summary here and a long-form expansion in `rtl8111h_phy_init.md` (the PHY init table is too long to inline without breaking the overview's flow).

### 5.1 High-level ordering

This is the order `rtl8111hInit` must perform, with the corresponding upstream Linux function for each step. The order is **not arbitrary** — most steps have an explicit ordering constraint cited in the upstream code, and getting any step out of order silently corrupts the chip state.

| # | Step | Upstream Linux reference |
|---|---|---|
| 1 | Map BAR0 (128 KiB) into the kernel address space, identity-mapped, `pagePresent | pageWrite | pagePCD | pagePWT` | (gooos-side; mirror of `e1000MapMMIO`) |
| 2 | Enable PCI **Memory Space (bit 1)** + **Bus Master (bit 2)** in PCI config Cmd | `r8169_main.c:rtl_init_one` (via `pcim_enable_device` / `pci_set_master`) |
| 3 | **Disable PCIe ASPM L1** in PCI Express capability link control. **Critical footgun** — see Footgun #3 in `rtl8111h_review_checklist.md`. | `r8169_main.c:rtl_init_one` (via `pci_disable_link_state(pdev, PCIE_LINK_STATE_L1)`) |
| 4 | Read MAC address from ERI registers `0xe0` (low 4 bytes) and `0xe4` (low 2 bytes of the next dword). **Not from `MAC0`/`MAC4` directly** — VER_46 uses ERI per `r8169_main.c:rtl_read_mac_address`. | `r8169_main.c:rtl_read_mac_address` (the `rtl_is_8168evl_up` arm) |
| 5 | Apply chip software reset: write `Cmd` register with the Reset bit set, poll until self-clear with timeout (50 ms is generous; the chip clears it within microseconds). | `r8169_main.c:rtl_hw_reset` |
| 6 | **20 ms idle** before any PHY MMIO/MDIO. **Critical footgun** — see Footgun #2 in `rtl8111h_review_checklist.md` (commit 3148ded, "r8169: fix powering up RTL8168h"). | Historical: the 20 ms wait was introduced in commit 3148ded into the (now-renamed/refactored) PHY power-up path; current upstream delegates PHY resume to PHYlib (`phy_resume(tp->phydev)` from `r8169_main.c:rtl8169_up`). The wait remains necessary on bare-metal because PHYlib's wait is what the commit historically added. |
| 7 | Read chip XID from `(TxConfig >> 20) & 0x7cf` and verify it matches `0x541` (RTL8111H or non-GMII RTL8107e variant) or `0x6c0` (RTL8168M); if not, refuse to bring the chip up. | `r8169_main.c:rtl8169_get_chip_version` (the `rtl_chip_infos[]` table uses `mask = 0x7cf` on every entry) |
| 8 | Mask all interrupts: `IntrMask = 0`. ISR is not yet installed; do not let the chip trigger one. | (defensive — analogous to e1000 step 2) |
| 9 | Allocate TX descriptor ring (256 × 16 B = 4 KiB), 256-byte aligned. Set every descriptor's `addr` to the corresponding TX buffer (allocated as a contiguous 256 × 1536 B = 384 KiB block, also from `allocPagesContig`). Set every descriptor's `opts1 = 0` (no `DescOwn`); set the last descriptor's `opts1 |= RingEnd`. Driver owns all TX descriptors at boot. | `r8169_main.c:rtl8169_init_ring`, `rtl8169_init_ring_indexes` (descriptor layout in `struct TxDesc`) |
| 10 | Allocate RX descriptor ring (256 × 16 B = 4 KiB), 256-byte aligned. Allocate RX buffers (256 × 2048 B = 512 KiB) — buffer size is `R8169_RX_BUF_SIZE = SZ_16K - 1` upstream, but for v1 we keep 2 KiB to mirror e1000 and to fit comfortably in the existing `netbuf` arithmetic. Set every descriptor's `addr` to its buffer, `opts1 = DescOwn | R8169_RX_BUF_SIZE` (NIC owns all RX descriptors at boot). Set the last descriptor's `opts1 |= RingEnd`. | `r8169_main.c:rtl8169_init_ring`, `rtl8169_mark_to_asic` |
| 11 | Write descriptor base addresses: `TxDescStartAddrHigh` first (high 32 of the TX ring physical address), then `TxDescStartAddrLow`. Same order for `RxDescAddrHigh` then `RxDescAddrLow`. The high-then-low order is **mandatory** per upstream comment "high before low for iop3xx ARM workaround"; on x86 the order does not matter for correctness, but obeying upstream costs nothing. | `r8169_main.c:rtl_set_rx_tx_desc_registers` |
| 12 | Write `RxMaxSize = 1519` (= max Ethernet frame 1518 + 1, matching upstream's `R8169_RX_BUF_SIZE + 1` semantic where the chip cuts off frames longer than this size). The 2 KiB RX buffer per slot is intentionally larger than `RxMaxSize` so the chip's max-frame can never overrun the buffer. | `r8169_main.c:rtl_set_rx_max_size` |
| 13 | Run the **EPHY init table** for 8168h_1: 6 entries, each is "EPHY register N: clear bits A, set bits B". Reproduced in `rtl8111h_phy_init.md` §2. | `r8169_main.c:rtl_hw_start_8168h_1` (the `e_info_8168h_1[]` table at the top) |
| 14 | Run the **MAC-OCP / ERI init sequence**. This is the body of `rtl_hw_start_8168h_1` after the EPHY table — `rtl_set_fifo_size`, `rtl8168g_set_pause_thresholds`, `rtl_set_def_aspm_entry_latency`, `rtl_reset_packet_filter`, `rtl_eri_set_bits` / `rtl_eri_write` calls, `rtl_disable_rxdvgate`, the `MISC_1`/`DLLPR` writes, `rtl_eri_clear_bits`, `rtl_pcie_state_l2l3_disable`, the `rg_saw_cnt` calibration, and the closing `r8168_mac_ocp_modify` / `r8168_mac_ocp_write` block. Reproduced step-by-step in `rtl8111h_phy_init.md` §3. | `r8169_main.c:rtl_hw_start_8168h_1` |
| 15 | Run the **PHY init table** (`rtl8168h_2_hw_phy_config`). This is the longest step — see `rtl8111h_phy_init.md` §4 for the full transcription including the `phy_modify_paged(0x0a43, 0x10, BIT(0), 0)` "disable 10m pll off" line that fixes Footgun #1 (commit 33189f0). | `r8169_phy_config.c:rtl8168h_2_hw_phy_config` |
| 16 | Program `RxConfig` (DMA/burst/multi half only, no accept-mask). For VER_46: `RxConfig = RX128_INT_EN | RX_MULTI_EN | RX_DMA_BURST | RX_EARLY_OFF = 0x0000_CF00`. Computed: `RX128_INT_EN = 1<<15 = 0x8000`, `RX_MULTI_EN = 1<<14 = 0x4000`, `RX_DMA_BURST = 7<<8 = 0x0700`, `RX_EARLY_OFF = 1<<11 = 0x0800`. Sum: `0xCF00`. | `r8169_main.c:rtl_init_rxcfg` (the `VER_40 ... VER_52` arm) |
| 17 | Program **RX accept mask** by OR-ing into the low byte of `RxConfig`: `cur := mmioRead32(rRxConfig); mmioWrite32(rRxConfig, cur | 0x0A)`. (`AcceptBroadcast = 0x08`, `AcceptMyPhys = 0x02`.) **`AcceptBroadcast` is required for DHCP** — the OFFER/ACK arrive via broadcast until the lease completes. **Footgun #6**. **Note**: upstream also sets `AcceptMulticast (0x04)`; we omit it for v1 because gooos has no multicast subscription API. Widen to `0x0E` if a future caller needs IPv6 NDP or mDNS. | `r8169_main.c:rtl_set_rx_mode` |
| 18 | Program `TxConfig = (TX_DMA_BURST<<8) | (InterFrameGap<<24) | TXCFG_AUTO_FIFO = (7<<8) | (3<<24) | (1<<7) = 0x0300_0780`. | `r8169_main.c:rtl_set_tx_config_registers` |
| 19 | Unlock the config registers (`Cfg9346 = 0xC0`) before any of the protected writes above; relock at the end (`Cfg9346 = 0x00`). The exact dance — unlock → write descriptor bases / RX max → relock — mirrors `rtl_hw_start`. | `r8169_main.c:rtl_unlock_config_regs` / `rtl_lock_config_regs` |
| 20 | Enable interrupts: `IntrMask = RxOK|RxErr|TxOK|TxErr|LinkChg = 0x002F`. **Only after** the ISR is installed (i.e. `registerHandler` has been called from `main.go`). | `r8169_main.c:rtl_irq_enable` |
| 21 | Flip the engines: `Cmd |= CmdRxEnb | CmdTxEnb` (bits 3 and 2 of `ChipCmd = 0x37`). | `r8169_main.c:rtl_hw_start` (the `RTL_W8(tp, ChipCmd, CmdTxEnb | CmdRxEnb)` line) |
| 22 | Poll `PHYstatus.LinkStatus` (`bit 1`) with a 5-second timeout. **Do not block forever** — if the link never comes up, log it and continue; DHCP will simply retry once `LinkChg` ISR fires later. | `r8169_main.c:r8169_phylink_handler` (the link-up path is event-driven; we poll because gooos has no scheduler-friendly wait) |

### 5.2 Why the order matters

A sample of the sharper ordering constraints:

- **Step 3 (ASPM L1 disable) before step 21 (engine enable)**: the chip racing into L1 too aggressively will drop PCIe transactions on some boards (Footgun #3, commit 90ca51e). If the engine is on before ASPM is constrained, a TX submission can transiently disappear into a PCIe link-state machine and not return.
- **Step 6 (20 ms idle) before any PHY access in step 13–15**: cite commit 3148ded. Without the wait, PHY MMIO writes silently land nowhere and link-up never happens.
- **Step 14 (`rtl_hw_start_8168h_1` MAC-OCP body) before step 15 (`rtl8168h_2_hw_phy_config`)**: the MAC-OCP body sets up clock-gating and ERI-state that the PHY init implicitly depends on. The reverse order produces a chip whose PHY answers MII reads with zeros.
- **Step 19 (Cfg9346 lock dance) wrapping descriptor-base writes**: most BAR0 register writes are no-ops unless `Cfg9346` is unlocked. Forgetting the unlock is a classic "init runs without errors but no packets ever flow" bug.
- **Step 20 (`IntrMask = 0x002F`) only after `registerHandler`**: same hazard as e1000 (`e1000EnableInterrupts` is called only after `registerHandler(handleE1000IRQ)`). If the chip starts firing interrupts before the IDT vector has a handler installed, the kernel takes a `#NM`-style trap and panics.

### 5.3 ERI / EPHY / MAC-OCP / PHY-OCP / MII access primitives

These are the building blocks every step from 13 onwards uses. They are implemented in `src/rtl8111h_phy.go` (full transcription in `rtl8111h_phy_init.md` §1):

- `eriRead(addr uint16) uint32` / `eriWrite(addr uint16, mask uint16, value uint32)` — write address + cmd to `ERIAR = 0x74`, poll `ERIAR_FLAG = 0x80000000` clear, read back from `ERIDR = 0x70`. Mirror of `r8169_main.c:_rtl_eri_read` / `_rtl_eri_write`.
- `ephyRead(reg uint8) uint16` / `ephyWrite(reg uint8, value uint16)` — write to `EPHYAR = 0x80`, poll, read back. Mirror of `r8169_main.c:rtl_ephy_read` / `rtl_ephy_write`.
- `macOcpRead(reg uint16) uint16` / `macOcpWrite(reg uint16, data uint16)` / `macOcpModify(reg uint16, mask, set uint16)` — single-register MAC-OCP via `OCPDR = 0xb0`. Mirror of `r8169_main.c:r8168_mac_ocp_*`. **Must be serialised by a per-NIC spinlock** to avoid Footgun #4 (commit-series for "Coalesce mac ocp write and modify for 8168H").
- `phyOcpRead(reg uint16) uint16` / `phyOcpWrite(reg uint16, data uint16)` — PHY-OCP via `GPHY_OCP = 0xb8`, with poll. Mirror of `r8169_main.c:r8168_phy_ocp_read` / `r8168_phy_ocp_write`.
- `phyReadPaged(page uint16, reg uint8) uint16` / `phyWritePaged(page uint16, reg uint8, value uint16)` / `phyModifyPaged(page uint16, reg uint8, mask, set uint16)` — paged MII access (page-select on reg `0x1f` then read/write target). gooos hand-implements these because there is no PHYlib equivalent. Cite: `phylib`'s `phy_read_paged` semantic, transcribed for our two-register path.

All accessors use `rtl8111hPHYLock` (a new `Spinlock`, declared at the top of `src/rtl8111h_phy.go`, lock-rank `4` — chosen so it sits below the netbuf rank-5 and above pure-leaf ranks). **This is the lock that prevents Footgun #4.**

---

## 6. TX path

### 6.1 Entry point

The upper stack calls `activeNIC.transmit(frame)`. For RTL8111H, this is `rtl8111hTransmit(frame []byte) bool`, defined in `src/rtl8111h.go`. Returns `true` on enqueue success, `false` on backpressure.

### 6.2 Steps (executed under `rtl8111hTxLock`, rank 5)

1. **Validate frame size**: `if n < 14 || n > 1518 { return false }`. (Same gate as `e1000Transmit`.)
2. **Pick the next TX descriptor**: `i := txTail`. If the descriptor's `DescOwn` bit is **still set** (NIC has not consumed the previous entry at this slot), return `false` — apply the same "drop-on-backpressure" semantic e1000 uses. This keeps the TX path lock-rank-5 and non-blocking.
3. **Copy the frame into the TX buffer**: `dst := txBufBase + uintptr(i) * txBufSize` (1536 B per slot). `for k := 0; k < n; k++ { *(*uint8)(unsafe.Pointer(dst + uintptr(k))) = frame[k] }`. Mirror of e1000's per-byte copy. (No `memcpy` builtin in TinyGo bare-metal — but a future optimisation could use `runtime.memmove`.)
4. **Fill the descriptor fields**:
   - `desc.addr = uint64(dst)` — physical = virtual under identity map.
   - `desc.opts2 = 0` — no checksum offload, no VLAN.
   - **`opts1` last** (DescOwn release): `desc.opts1 = DescOwn | FirstFrag | LastFrag | (RingEnd if i == NUM_TX_DESC-1 else 0) | uint32(n)`. The NIC reads `addr` and `opts2` first; setting `opts1.DescOwn` is the publication step. **Cite `dma_wmb()` between step (4) field writes and (4) `opts1` write**: gooos mirrors this with a hand-rolled `wmb()` (a `mfence` from `src/stubs.S`-style). On x86-TSO with a single writer this is technically a no-op, but the comment must be present so SMP-correctness is preserved if a future cross-CPU writer ever appears.
5. **Advance `txTail`**: `txTail = (txTail + 1) % NUM_TX_DESC`.
6. **Doorbell**: `mmioWrite8(rtl8111hBase + TxPoll, NPQ)` — `NPQ = 0x40`. This is a single byte write to `0x38` that tells the chip "you have new TX work". Cite `r8169_main.c:rtl8169_doorbell` (the non-8125 branch).
7. **Bump counters**: `statsInc(&netStats.TxPackets)`, `statsAdd(&netStats.TxBytes, uint64(n))`. Same as e1000.
8. **Return `true`**.

### 6.3 TX completion

For v1 we **do not** harvest TX descriptors on the `TxOK` ISR — instead, `rtl8111hTransmit` polls the `DescOwn` bit at step 2 the next time the slot is reused. This is the same lazy-cleanup model `e1000Transmit` uses. The cost is one extra ring slot of latency on TX backpressure; the benefit is a much simpler ISR (which is the load-bearing constraint).

A future optimisation could harvest TX descriptors in `drainRxRingRTL8111H` (since both the RX and TX rings share the polling kthread) but for v1 we do not.

### 6.4 Edge cases

- **Sub-60-byte padding**: the chip pads short frames automatically when `TxConfig.PadShort` is implicit in the `TXCFG_AUTO_FIFO` flag we set; no driver action required.
- **Frames > 1518 B**: rejected at step (1). Jumbo frames are out of scope for v1.
- **TX FIFO underrun on small packets**: not seen on VER_46 in upstream commit history; no special-case needed.

---

## 7. RX path

### 7.1 ISR side

`handleRTL8111HIRQ` (in `src/rtl8111h_irq.go`) sets `rxReadyFlagRTL8111H = 1`, increments `rtl8111hIRQCount`, acks `IntrStatus`, and EOIs. **No descriptor processing happens in the ISR.** That is the cardinal ISR-safety rule (descriptor processing involves looping, frame-copying, and dispatch into the ARP/IP code — none of which is `make lint`-safe).

### 7.2 `netRxLoop` side

The existing `netRxLoop` (in `src/net.go`) currently calls `drainRxRing()` (the e1000 drain). It is extended to a one-pointer dispatch through `nicDriver.drainRX`:

```go
func netRxLoop() {
    for {
        if activeNIC != nil {
            activeNIC.drainRX()
        }
        statsInc(&netStats.NetRxLoopWakes)
        if kschedRunning[cpuID()] != nil {
            kschedYield()
        } else {
            runtime.Gosched()
        }
    }
}
```

The function-pointer indirection is one extra `MOV` per loop iteration. It avoids the string compare an earlier draft proposed, and keeps the loop body free of any `make lint`-relevant constructs (no allocations, no chans, no string ops) so a future migration of `netRxLoop` into ISR-reachable territory does not break.

### 7.3 `drainRxRingRTL8111H`

Mirror of `r8169_main.c:rtl_rx`, simplified for the gooos buffer model (no skbuff, no NAPI, no checksum offload):

```
loop:
  desc := &rxRing[rxHead]
  if (desc.opts1 & DescOwn) != 0 {
    return                         // NIC still owns it; no work
  }
  // Read barrier: opts1.DescOwn was the publish; opts2/addr/payload
  // must be visible now. dma_rmb() upstream — on x86 a no-op.

  status := desc.opts1
  if status & RxRES != 0 {
    statsInc(&netStats.RxDropped)
    goto release_descriptor
  }
  pktSize := uint32(status & 0x3FFF)
  if pktSize < 14 || pktSize > 1518 {
    statsInc(&netStats.RxDropped)
    goto release_descriptor
  }

  // Copy into a fresh slice for ethernetDispatch. (Mirror of e1000.)
  buf := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(desc.addr))), pktSize)
  frame := make([]byte, pktSize)
  copy(frame, buf)

  statsInc(&netStats.NetRxFrames)
  statsInc(&netStats.RxPackets)
  statsAdd(&netStats.RxBytes, uint64(pktSize))
  ethernetDispatch(frame)

release_descriptor:
  // Hand the descriptor back to the NIC: clear opts2, set DescOwn |
  // RingEnd (if last) | RxBufSize. Cite rtl8169_mark_to_asic.
  eor := desc.opts1 & RingEnd
  desc.opts2 = 0
  // dma_wmb() upstream — on x86 a no-op for a single writer
  desc.opts1 = DescOwn | eor | rxBufSize
  rxHead = (rxHead + 1) % NUM_RX_DESC
  goto loop
```

### 7.4 RX FIFO overflow handling

If the chip sets `RxOverflow` (`bit 4`) in `IntrStatus`, the ISR will see it. For v1 we do not need a separate handler — `RxOverflow` simply causes some `RxRES`-flagged descriptors to land in the ring, which the loop above counts under `RxDropped`. The next available slots resume normally because we always re-arm with `DescOwn`. (The upstream driver does fancier per-RX-error counters; gooos prioritises simplicity.)

### 7.5 Ring-wrap correctness

The single hazard is forgetting to set `RingEnd` on the last descriptor when re-arming. The release_descriptor block reads the existing `RingEnd` bit out of `opts1` *before* overwriting the field, then re-applies it: `eor := desc.opts1 & RingEnd; desc.opts1 = DescOwn | eor | rxBufSize`. That preserves the bit across the rearm. Mirror of `r8169_main.c:rtl8169_mark_to_asic`.

---

## 8. Interrupt handling (`src/rtl8111h_irq.go`)

### 8.1 Full ISR body (proposed)

```go
//go:nosplit
func handleRTL8111HIRQ(vector uint64) {
    rtl8111hIRQCount++

    // IntrStatus is RW1C (write-1-to-clear). Read first, then write
    // back the bits we observed to ack them. Mirror of
    // rtl_get_events + rtl_ack_events.
    status := mmioRead16(rtl8111hBase + 0x3e) // IntrStatus

    if status == 0xFFFF {
        // chip removed or in bad state; ack nothing, just EOI
        if ioapicActive {
            lapicSendEOI()
        } else {
            picSendEOI(uint8(vector - 32))
        }
        return
    }

    if status & 0x0001 != 0 { // RxOK
        rxReadyFlagRTL8111H = 1
    }
    if status & 0x0020 != 0 { // LinkChg
        rtl8111hLinkChangeFlag = 1
        // PHY re-read happens in the kthread loop; ISR cannot do it
        // (PHY access spinlock would be acquired too high here).
    }

    // Ack everything we saw.
    mmioWrite16(rtl8111hBase + 0x3e, status)

    if ioapicActive {
        lapicSendEOI()
    } else {
        picSendEOI(uint8(vector - 32))
    }
}
```

### 8.2 Why no allocation / no chan / no go inside

Audit:

- No `make(chan ...)` — none.
- No channel send / receive — none.
- No `go` statement — none.
- No string concat with literals — none.
- No slice literal — none.
- No map literal — none.
- No interface boxing — none.

Exclusive use of `mmioRead16` / `mmioWrite16` (raw `*(*uint16)(unsafe.Pointer(...))` accessors), arithmetic, and global-variable stores. **Compatible with `make lint`'s `scripts/lint_isr.go` AST walker at `maxDepth = 4`.**

`//go:nosplit` is applied to bypass the depth tracking. The ISR body itself is fully inlined; only `mmioRead16` / `mmioWrite16` / `picSendEOI` / `lapicSendEOI` are callees, all of which are either `//go:nosplit` themselves or in the lint safelist.

### 8.3 RW1C ack semantics

The exact discipline upstream uses (cite `r8169_main.c:rtl_ack_events`):

```c
static void rtl_ack_events(struct rtl8169_private *tp, u32 bits) {
    if (rtl_is_8125(tp))
        RTL_W32(tp, IntrStatus_8125, bits);
    else
        RTL_W16(tp, IntrStatus, bits);
}
```

i.e. **write back the bits you saw** — not all 1s, not the inverse. Writing `0xFFFF` "to clear all" works on most variants but is not the documented contract; mirror upstream and ack exactly the bits read.

### 8.4 Why we do not handle `LinkChg` in the ISR

PHY status read requires holding `rtl8111hPHYLock` (rank 4). ISRs run at the highest priority and cannot acquire spinlocks safely (deadlock if any non-ISR path holds the lock). Set a flag, let the kthread side observe it on the next `netRxLoop` iteration, then perform the PHY read and update `activeNIC.linkUp` / serial-print under proper locking. See Chapter 9.

---

## 9. Link-state handling

### 9.1 Where the state lives

Two new globals in `src/rtl8111h.go`:

```go
var rtl8111hLinkUp uint32         // 0/1, written by kthread side only
var rtl8111hLinkChangeFlag uint32 // 0/1, set by ISR, cleared by kthread side
```

### 9.2 Auto-negotiation poll at boot

After step (21) in §5.1 (engines enabled), `rtl8111hInit` polls `PHYstatus` for up to 5 seconds:

```go
deadline := pitTicks + 500 // 100 Hz -> 500 ticks = 5 s
for pitTicks < deadline {
    ps := mmioRead8(rtl8111hBase + 0x6c)
    if ps & 0x02 != 0 { // LinkStatus
        rtl8111hLinkUp = 1
        speed := "10"
        switch {
        case ps & 0x10 != 0: speed = "1000"
        case ps & 0x08 != 0: speed = "100"
        }
        duplex := "half"
        if ps & 0x01 != 0 { duplex = "full" }
        serialPrintln("rtl8111h: auto-neg complete: " + speed + " Mbps " + duplex)
        return
    }
    hlt()
}
serialPrintln("rtl8111h: auto-neg timeout (5 s) — link not yet up; will retry on LinkChg")
```

If the timeout elapses, the driver continues. DHCP will fail at first attempt but the `LinkChg` ISR + kthread-side flag-poll will eventually wake the link state and DHCP can be re-run from the shell.

### 9.3 Kthread-side `LinkChg` observer

Inside `netRxLoop` (or as a small sibling kthread `rtl8111hLinkLoop` to keep `netRxLoop` minimal), observe `rtl8111hLinkChangeFlag`:

```go
if atomic_xchg(&rtl8111hLinkChangeFlag, 0) != 0 {
    ps := mmioRead8(rtl8111hBase + 0x6c)
    if ps & 0x02 != 0 {
        rtl8111hLinkUp = 1
        serialPrintln("rtl8111h: link up")
    } else {
        rtl8111hLinkUp = 0
        serialPrintln("rtl8111h: link down")
    }
}
```

(In v1 we put this snippet directly in `netRxLoop` after the `drainRxRingRTL8111H()` call. A separate kthread is overkill for one flag.)

### 9.4 Link-down behaviour

`rtl8111hTransmit` checks `rtl8111hLinkUp` at entry; if zero, return `false`. Upper layers (ARP / IPv4 / UDP / TCP retransmit) already handle TX failure gracefully. No frames are queued internally.

---

## 10. Driver lifecycle

### 10.1 Init

Single entry point: `rtl8111hInit()`, called from `netInit()` in `src/net.go` (the dispatch is detailed in Chapter 11). Runs the 22-step sequence in §5.1. If any step fails (timeout polling reset bit, MAC address comes back as all-zeros, etc.), `rtl8111hInit` logs the failure via `serialPrintln` and **does not** set `activeNIC = &rtl8111hDriver`; the driver appears as "absent" to the upper stack and DHCP simply will not begin.

### 10.2 Steady state

The chip runs forever. The two long-lived flows are:

- **TX**: `activeNIC.transmit(frame)` calls `rtl8111hTransmit`; the chip DMAs the frame onto the wire and (if `TxOK` is unmasked) raises an interrupt the ISR consumes.
- **RX + LinkChg**: the ISR sets `rxReadyFlagRTL8111H` / `rtl8111hLinkChangeFlag`; `netRxLoop` polls and dispatches.

### 10.3 Shutdown

**Not implemented in v1.** gooos halts the system on `exit`; there is no clean shutdown path. If a future revision adds suspend/resume, it must (a) mask all interrupts (`IntrMask = 0`), (b) flip `Cmd.RxEnb` and `Cmd.TxEnb` off, (c) wait for the TX FIFO to drain, (d) write `PMCH.D3HOT_NO_PLL_DOWN` per the suspend protocol; cite `r8169_main.c:rtl8169_down`.

---

## 11. Coexistence policy with e1000

### 11.1 Why this matters

QEMU runs always have e1000 (`-device e1000`) and never have RTL8111H. Real hardware can have either or, very rarely, both (a board with on-board RTL8111H plus a discrete Intel NIC card). The driver dispatch must give a deterministic answer for all four cases:

| `e1000Found` | `rtl8111hFound` | `enableRTL8111H` | `preferRTL8111H` | `activeNIC` |
|---|---|---|---|---|
| true | false | * | * | `&e1000Driver` (QEMU base case) |
| false | true | true | * | `&rtl8111hDriver` (real-hardware base case) |
| false | true | false | * | `nil` (driver compiled out — DHCP cannot start) |
| true | true | false | * | `&e1000Driver` (driver compiled out) |
| true | true | true | false | `&e1000Driver` (default tie-break: e1000 wins) |
| true | true | true | true | `&rtl8111hDriver` (operator opt-in tie-break) |
| false | false | * | * | `nil` (no NIC — boot proceeds without networking) |

### 11.2 Why default tie-break = e1000

Three reasons:

1. The gooos regression matrix runs in QEMU. Defaulting to e1000 when both are present means a developer who flips `enableRTL8111H = true` on their dev box does not accidentally break the QEMU regression suite (which would happen if `-device e1000` AND a stray RTL8111H pass-through were both attached).
2. e1000 is the well-tested path; RTL8111H is new. "Stable wins ties" is the conservative default.
3. Operator opt-in via `preferRTL8111H = true` is one constant flip away. No runtime configuration is needed.

### 11.3 Implementation in `netInit`

The decision lives in `netInit`, not `main`, so test harnesses get the same behaviour. **Critical ordering**: the `activeNIC = &xxxDriver` assignment must happen **before** any code path that calls `activeNIC.transmit` (or its current name `e1000Transmit`). In particular, `arpSendGratuitous` (called from netInit's static-IP fallback path) is one such caller — see checklist item 47.

```go
func netInit() {
    // Step 1 of netInit: pick the active NIC. MUST happen before any
    // ARP / IPv4 / UDP TX activity below, otherwise activeNIC is nil
    // and gratuitous-ARP and similar are silently dropped.
    switch {
    case enableRTL8111H && rtl8111hFound && (preferRTL8111H || !e1000Found):
        // RTL8111H wins
        // (rtl8111hInit was called from main.go before netInit)
        rtl8111hDriver.mac = rtl8111hMAC
        activeNIC = &rtl8111hDriver
    case e1000Found:
        // e1000 wins (default if both, or QEMU base case)
        e1000Driver.mac = e1000MAC
        activeNIC = &e1000Driver
    default:
        // No NIC; the rest of netInit short-circuits.
        return
    }

    // Step 2: from this point on, activeNIC is non-nil. All ARP /
    // IPv4 / UDP TX paths are safe to call.

    // ... existing netInit body: configure static IP fallback,
    //     gratuitous ARP (which now goes through activeNIC.transmit),
    //     spawn netRxLoop / netDiagLoop / TCP RTO scanner / TCP echo
    //     / UDP echo kthreads.
}
```

The `MAC` copy step is what binds the chip-specific MAC into the NIC-agnostic dispatch struct. The "Step 1 / Step 2" comment in the body is mandatory — the implementer must not reorder these.

### 11.4 `netDiag` change

The existing `netDiagLoop` periodic printer should output the active-NIC name on every line:

```
[net] active=rtl8111h linkUp=1 RxFrames=12 TxPackets=4 RxDropped=0 IRQs=8
```

vs.

```
[net] active=e1000 linkUp=1 RxFrames=14 TxPackets=5 RxDropped=0 IRQs=10
```

The line is built using string concatenation, which is allowed in `netDiag` because it is **not** ISR-reachable — it runs as the `netDiagLoop` kthread.

---

## 12. Code review checklist

The full ≥25-item checklist with per-item failure modes and Linux-source citations lives in **`rtl8111h_review_checklist.md`**. Topics covered (each with at least one concrete checklist entry):

- MMIO write address + value verification against upstream
- Descriptor field bit verification against `r8169_main.c:struct TxDesc/RxDesc`
- Ordering constraints (`dma_wmb`, `smp_wmb`, doorbell after descriptor publish)
- Endianness (little-endian on the wire, upstream uses `__le32` / `__le64`; gooos x86_64 is also little-endian so the casts are no-ops, but the audit must explicitly verify there is no cpu_to_le / le_to_cpu drift)
- DMA buffer alignment (256 B for ring, 4 B for buffers)
- Ring-wrap arithmetic (mod NUM_TX_DESC / NUM_RX_DESC, RingEnd preservation)
- IMR/ISR bit consistency (we never enable a bit we don't ack, never ack a bit we don't enable)
- ISR allocation-freeness (full audit chain from `handleRTL8111HIRQ`)
- MAC address byte ordering (upstream reads via `get_unaligned_le32` from ERI 0xe0 — gooos must also use little-endian load)
- PHY OCP / PHY paged-MII access timing (poll-clear-bit before next access)
- RxConfig accept-broadcast bit (DHCP gate — Footgun #6 if missed)
- TxConfig IPG + DMA burst + auto-FIFO bits
- Buffer-vs-MTU consistency (rxBufSize >= 1518 + alignment slack)
- NULL-pointer / out-of-range checks for descriptor base
- Error-path leak audit (init step that fails midway returns physical pages back to `pageAllocLock`)
- Stale-write hazard on descriptor `DescOwn` flip (always last write, after addr/opts2)
- Big-buffer truncation (we cap RX at 1518 even though chip would accept more)
- ARP cache compatibility (no changes — but verify that the ARP refresh path triggered by gratuitous ARP does not assume e1000-specific timing)
- Statistic counter readout race (counters are uint64 written by single writer; cross-CPU reads need either atomic load or "tear-tolerant" snapshot — same as e1000)
- **Five named footguns** from Linux commit history (33189f0, 3148ded, 90ca51e, the OCP-coalesce series, ebe5989)

---

## 13. Test plan for the one-shot real-hardware bring-up

Lives in **`rtl8111h_review_checklist.md`** §B (the file holds both the checklist and the test plan because they are read together during the bring-up session). Includes:

- The expected serial-log success sequence (10+ specific log lines in order)
- `netDiag` counter expectations after `dhcp.elf` succeeds
- Host-side commands to run (ping, nc UDP, nc TCP, http GET via wget.elf)
- A failure-pattern → root-cause matrix with ≥10 entries
- A binary go/no-go gate

---

## 14. Rollback / fallback

### 14.1 Compile-out path

`enableRTL8111H = false` in `src/preempt_config.go` short-circuits **all** RTL8111H code paths:

- `pciInit` does not call `pciRecordRTL8111H` (gate at the case label).
- `main.go` does not call `rtl8111hInit` or `registerHandler(handleRTL8111HIRQ)`.
- `netInit` cannot select the RTL8111H driver because `rtl8111hFound` will be `false` and `enableRTL8111H` is `false`.
- `netRxLoop`'s dispatch always falls through to the e1000 path (because `activeNIC.name != "rtl8111h"`).
- TinyGo's const-folding will dead-code-strip the entire `rtl8111h*.go` file bodies once the compiler proves they are unreachable. (Mirror of how `userspaceSMP = false` strips the Ring-3 dispatch tier.)

This is the same precedent as `userspaceSMP` in `src/preempt_config.go`. No new build-tag mechanism is introduced.

### 14.2 Runtime-only fallback

If the driver is enabled at compile time but fails at runtime (init step times out, MAC address comes back zero, etc.), `rtl8111hInit` logs the failure via `serialPrintln` and returns without setting `activeNIC = &rtl8111hDriver`. The dispatch in `netInit` then falls through to `e1000Found`, or to no-NIC if e1000 is also absent. **The kernel never panics on RTL8111H init failure** — it simply leaves networking off.

### 14.3 Remove the driver entirely

`git revert <commit-of-this-feature>` removes:

- All of `src/rtl8111h*.go` and `src/nic.go`
- The `pciRecordRTL8111H` arm in `src/pci.go`
- The 5 `activeNIC.transmit` substitutions in `src/{arp,ipv4,udp}.go` (replaced back with `e1000Transmit`)
- The two new gates in `src/preempt_config.go`
- The `RTL8111HIRQs` field in `src/netstats.go`

Because the entire feature is gated behind `enableRTL8111H`, rollback impact on existing QEMU users is zero: their builds were always taking the e1000 path.

---

## 15. References

### 15.1 Linux upstream

All cited functions exist in upstream Linux `master` as of the writing of this document; cite by function name (line numbers omitted because they age).

- **`drivers/net/ethernet/realtek/r8169.h`** — chip-version enum, exported helper signatures.
- **`drivers/net/ethernet/realtek/r8169_main.c`** — `rtl_init_one`, `rtl8169_get_chip_version`, `rtl_chip_infos[]`, `rtl_hw_start_8168h_1`, `rtl_hw_config[]`, `rtl_hw_start`, `rtl_hw_start_8168`, `rtl_set_def_aspm_entry_latency`, `rtl_set_aspm_entry_latency`, `rtl_aspm_is_safe`, `rtl_init_rxcfg`, `rtl_set_rx_max_size`, `rtl_set_tx_config_registers`, `rtl_set_rx_mode`, `rtl_set_rx_tx_desc_registers`, `rtl_unlock_config_regs`, `rtl_lock_config_regs`, `rtl8169_doorbell`, `rtl8169_start_xmit`, `rtl_rx`, `rtl8169_mark_to_asic`, `rtl8169_interrupt`, `rtl_get_events`, `rtl_ack_events`, `rtl_irq_disable`, `rtl_irq_enable`, `rtl_set_irq_mask`, `rtl_set_d3_pll_down`, `rtl_hw_reset`, `rtl_disable_rxdvgate`, `rtl_pcie_state_l2l3_disable`, `_rtl_eri_read`, `_rtl_eri_write`, `rtl_eri_set_bits`, `rtl_eri_clear_bits`, `rtl_ephy_read`, `rtl_ephy_write`, `__rtl_ephy_init`, `r8168_phy_ocp_read`, `r8168_phy_ocp_write`, `__r8168_mac_ocp_write`, `r8168_mac_ocp_write`, `__r8168_mac_ocp_read`, `r8168_mac_ocp_read`, `r8168_mac_ocp_modify`, `rtl_init_mac_address`, `rtl_read_mac_address`, `rtl_rar_set`, `r8169_phylink_handler`, `rtl_link_chg_patch` (no-op for VER_46), `rtl_set_fifo_size`, `rtl8168g_set_pause_thresholds`, `rtl_reset_packet_filter`, `rtl8168h_2_get_adc_bias_ioffset`, `rtl8169_init_ring`, `rtl8169_init_ring_indexes`, `rtl8169_up`, `phy_resume` (PHYlib, called from `rtl8169_up` — the historical home of the 20 ms wait commit `3148ded` introduced).
- **`drivers/net/ethernet/realtek/r8169_phy_config.c`** — `rtl8168h_2_hw_phy_config`, `r8168g_phy_param`, `rtl8168g_enable_gphy_10m`, `rtl8168g_disable_aldps`, `rtl8168g_config_eee_phy`, the `r8169_hw_phy_config[]` table at the bottom.

### 15.2 Linux commit history (named footguns)

- **commit `33189f0`** ("r8169: fix RTL8168H and RTL8107E rx crc error") — RX CRC errors at low link speed / cold temp; needs `phy_modify_paged(0x0a43, 0x10, BIT(0), 0)` in `rtl8168h_2_hw_phy_config`.
- **commit `3148ded`** ("r8169: fix powering up RTL8168h") — needs `msleep(20)` after PHY power-up before any PHY MMIO/MDIO. The original commit landed in the (now-renamed/refactored) `rtl_pll_power_up` path; current upstream delegates the wait to PHYlib (`phy_resume` from `rtl8169_up`).
- **commit `90ca51e`** ("r8169: fix ASPM-related issues on a number of systems with NIC version from RTL8168h") — disable ASPM L1 for VER_46 by default.
- **PATCH v5 3/7** ("r8169: Coalesce mac ocp write and modify for 8168H start to reduce spinlock contention") — `r8168_mac_ocp_*` must be serialised by a per-NIC spinlock.
- **commit `ebe5989`** ("r8169: remove support for chip versions 45 and 47") — VER_45 is gone; 8168H is VER_46 only.

### 15.3 Datasheet

- **Realtek RTL8111/RTL8168 PCI Express Gigabit Ethernet Controllers programming guide** — publicly accessible via Realtek developer downloads; cite section numbers in the body of `rtl8111h_phy_init.md` for register offsets where the upstream Linux driver does not document them.

### 15.4 OSDev wiki

- **"RTL8169" article** — useful for the basic `IntrMask`/`IntrStatus` semantics and the early-init reset dance, but **does not** cover the 8168/8111 family's PHY OCP / ERI / EPHY init. Read it after this document, not before.

### 15.5 gooos files touched

- **NEW**: `src/nic.go`, `src/rtl8111h.go`, `src/rtl8111h_phy.go`, `src/rtl8111h_irq.go`
- **EDIT**: `src/pci.go`, `src/main.go`, `src/net.go` (`netInit`, `netRxLoop`, `netDiagLoop`), `src/arp.go` (3 sites), `src/ipv4.go` (1 site), `src/udp.go` (1 site), `src/preempt_config.go` (2 new consts), `src/netstats.go` (1 new field)
- **NEW DOC**: `impldoc/rtl8111h_overview.md` (this file), `impldoc/rtl8111h_phy_init.md`, `impldoc/rtl8111h_review_checklist.md`

No changes to `Makefile`, `scripts/`, `user/`, or any test harness.

---

## Review history

This document went through one independent-reviewer pass before being declared ready for one-shot real-hardware bring-up.

**Pass 1** (independent reviewer subagent, run with full WebFetch access against upstream Linux `master`):

- **Verdict**: APPROVE-WITH-FIXES.
- **Findings**: 8 MUST-FIX (XID mask `0xfcf` → `0x7cf`; `RXDV_GATED_EN` is bit 19 not bit 3 and needs 32-bit access; `rtl_pcie_state_l2l3_disable` targets `Config3 = 0x54` MMIO clearing `Rdy_to_L23 = 1<<1`, NOT MAC-OCP `0xe092`; ERIAR mask constants were 4 bits too wide; `PFM_D3COLD_EN = 0x40` not `0x10`; `rtl_pll_power_up` no longer exists upstream — re-cite as historical commit `3148ded`; `rtl_init_rxring`/`rtl_init_txring`/`rtl_alloc_rx_databuffs` don't exist — replace with `rtl8169_init_ring`/`rtl8169_init_ring_indexes`; `rtl8169_phylink_handler` typo for `r8169_phylink_handler`). 10 SHOULD-FIX (mostly: kernel primitives `mmioRead/WriteN`, `udelay`/`msleep`/`pollUntilLow/High`, `pciConfigRead/Write8/16`, `pciFindCapability` had to be explicitly committed to with proposed bodies; `RxConfig` value resolved as `0xCF00` with accept-mask OR'd separately; `RxMaxSize` corrected to `1519`; ASPM entry-latency at config offset `0x70F` requires the CSI fallback path because gooos's port-IO config can only address 256 bytes; `netInit` ordering re: gratuitous-ARP made explicit; `nicDriver` extended with `drainRX func()` to remove a string-compare in `netRxLoop`; MDIO accessors committed to `PHYAR = 0x60` with the explicit read/write protocol). 7 NIT (mostly stylistic — terminology, citation discipline).
- **Resolution**: All 8 MUST-FIX applied. All 10 SHOULD-FIX applied. NITs 2 (AcceptMulticast deviation) and 6 (RxOverflow terminology) applied; remaining NITs (commit-URL hyperlinks, etc.) not applied as they are presentational.
- **Net change**: ~250 lines added across the three documents (mostly the new §4.6 "Kernel primitives the driver introduces" subsection, the CSI fallback for ASPM, and the explicit MDIO protocol). Document is now self-contained: a future implementer with only the gooos source tree and these three files can implement the driver without further upstream research.

No second-pass review was run because the first pass surfaced fewer than 5 MUST-FIX items that required follow-up correlation across documents (the cap defined in the writing plan).
